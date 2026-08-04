package dbconn

/*
 * This file contains structs and functions related to connecting to a database
 * and executing queries.
 */

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/warehouse-pg/common-go-libs/gplog"
	"github.com/warehouse-pg/common-go-libs/operating"

	/*
	 * We previously used github.com/lib/pq as our Postgres driver,
	 * but it had a bug with the way it handled certain encodings.
	 * pgx seems to handle these encodings properly.
	 */
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// SQLSTATEs surfaced by handleConnectionError. pgx wraps server errors in a
// *pgconn.PgError; classifying by code is more reliable than substring
// matching the message text.
const (
	sqlstateUndefinedObject   = "42704" // nonexistent role, unrecognized GUC
	sqlstateUndefinedDatabase = "3D000"
)

/*
 * While the sql.DB struct maintains its own connection pool, there is no
 * guarantee of session-level consistency between queries and we require that
 * level of control in some cases.
 *
 * Thus, DBConn maintains its own connection pool of sql.DBs (all set to have
 * exactly one database connection each) in an array, such that callers can
 * create NumConns goroutines and assign each an index from 0 to NumConns to
 * guarantee that each goroutine gets its own connection that exhibits single-
 * session behavior.  The Exec, Select, and Get functions are set up to default
 * to the first connection (index 0), so the DBConn will still exhibit session-
 * like behavior if no connection is specified, and other functions that want to
 * execute in serial should pass in a 0 wherever a connection number is needed.
 */
type DBConn struct {
	ConnPool []*sql.DB
	NumConns int
	Driver   DBDriver
	User     string
	DBName   string
	Host     string
	Port     int
	Tx       []*sql.Tx
	Version  GPDBVersion
}

/*
 * Structs and functions for testing database functions
 */

type DBDriver interface {
	Connect(driverName string, dataSourceName string) (*sql.DB, error)
}

type GPDBDriver struct {
}

/*
 * Connect opens the database and verifies it is reachable, so that a bad
 * connection surfaces here rather than on the first query.  sql.Open alone is
 * lazy and would defer the error.
 */
func (driver *GPDBDriver) Connect(driverName string, dataSourceName string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

/*
 * Database functions
 */

func NewDBConnFromEnvironment(dbname string) *DBConn {
	if dbname == "" {
		gplog.Fatal(errors.New("No database provided"), "")
	}

	username := operating.System.Getenv("PGUSER")
	if username == "" {
		currentUser, _ := operating.System.CurrentUser()
		username = currentUser.Username
	}
	host := operating.System.Getenv("PGHOST")
	if host == "" {
		host, _ = operating.System.Hostname()
	}
	port, err := strconv.Atoi(operating.System.Getenv("PGPORT"))
	if err != nil {
		port = 5432
	}

	return NewDBConn(dbname, username, host, port)
}

func NewDBConn(dbname, username, host string, port int) *DBConn {
	if dbname == "" {
		gplog.Fatal(errors.New("No database provided"), "")
	}

	if username == "" {
		gplog.Fatal(errors.New("No username provided"), "")
	}

	if host == "" {
		gplog.Fatal(errors.New("No host provided"), "")
	}

	return &DBConn{
		ConnPool: nil,
		NumConns: 0,
		Driver:   &GPDBDriver{},
		User:     username,
		DBName:   dbname,
		Host:     host,
		Port:     port,
		Tx:       nil,
		Version:  GPDBVersion{},
	}
}

func (dbconn *DBConn) MustBegin(whichConn ...int) {
	err := dbconn.Begin(whichConn...)
	gplog.FatalOnError(err)
}

func (dbconn *DBConn) Begin(whichConn ...int) error {
	connNum := dbconn.ValidateConnNum(whichConn...)
	if dbconn.Tx[connNum] != nil {
		return errors.New("Cannot begin transaction; there is already a transaction in progress")
	}
	tx, err := dbconn.ConnPool[connNum].BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	dbconn.Tx[connNum] = tx
	return nil
}

func (dbconn *DBConn) Close() {
	if dbconn.ConnPool != nil {
		for _, conn := range dbconn.ConnPool {
			if conn != nil {
				_ = conn.Close()
			}
		}
		dbconn.ConnPool = nil
		dbconn.Tx = nil
		dbconn.NumConns = 0
	}
}

func (dbconn *DBConn) MustCommit(whichConn ...int) {
	err := dbconn.Commit(whichConn...)
	gplog.FatalOnError(err)
}

func (dbconn *DBConn) Commit(whichConn ...int) error {
	connNum := dbconn.ValidateConnNum(whichConn...)
	if dbconn.Tx[connNum] == nil {
		return errors.New("Cannot commit transaction; there is no transaction in progress")
	}
	err := dbconn.Tx[connNum].Commit()
	dbconn.Tx[connNum] = nil
	return err
}

func (dbconn *DBConn) MustRollback(whichConn ...int) {
	err := dbconn.Rollback(whichConn...)
	gplog.FatalOnError(err)
}

func (dbconn *DBConn) Rollback(whichConn ...int) error {
	connNum := dbconn.ValidateConnNum(whichConn...)
	if dbconn.Tx[connNum] == nil {
		return errors.New("Cannot rollback transaction; there is no transaction in progress")
	}
	err := dbconn.Tx[connNum].Rollback()
	dbconn.Tx[connNum] = nil
	return err
}

func (dbconn *DBConn) MustConnect(numConns int) {
	err := dbconn.Connect(numConns)
	gplog.FatalOnError(err)
}

func (dbconn *DBConn) Connect(numConns int, utilityMode ...bool) error {
	if numConns < 1 {
		return errors.New("Must specify a connection pool size that is a positive integer")
	}
	if dbconn.ConnPool != nil {
		return errors.New("The database connection must be closed before reusing the connection")
	}

	dbname := EscapeConnectionParam(dbconn.DBName)
	user := EscapeConnectionParam(dbconn.User)
	krbsrvname := operating.System.Getenv("PGKRBSRVNAME")
	if krbsrvname == "" {
		krbsrvname = "postgres"
	}
	sslmode := operating.System.Getenv("PGSSLMODE")
	if sslmode == "" {
		sslmode = "prefer"
	}
	// This string takes in the literal user/database names. They do not need
	// to be escaped or quoted.
	//
	// statement_cache_capacity=0 disables pgx's automatic prepared-statement
	// cache: re-creating an object with the same name in one session triggers
	// a cache-lookup failure on GPDB 4 otherwise. default_query_exec_mode=exec
	// keeps queries on the simple protocol so the server sees them as
	// individual statements.
	connStr := fmt.Sprintf(`user='%s' dbname='%s' krbsrvname='%s' host=%s port=%d sslmode='%s' statement_cache_capacity=0 default_query_exec_mode=exec`,
		user, dbname, krbsrvname, dbconn.Host, dbconn.Port, sslmode)

	dbconn.ConnPool = make([]*sql.DB, numConns)
	if len(utilityMode) > 1 {
		return errors.New("The utility mode parameter accepts exactly one boolean value")
	} else if len(utilityMode) == 1 && utilityMode[0] {
		var err error
		connStr, err = dbconn.utilityModeConnStr(connStr)
		if err != nil {
			return err
		}
	}

	for i := 0; i < numConns; i++ {
		conn, err := dbconn.Driver.Connect("pgx", connStr)
		err = dbconn.handleConnectionError(err)
		if err != nil {
			return err
		}
		conn.SetMaxOpenConns(1)
		conn.SetMaxIdleConns(1)
		dbconn.ConnPool[i] = conn
	}
	dbconn.Tx = make([]*sql.Tx, numConns)
	dbconn.NumConns = numConns
	version, err := InitializeVersion(dbconn)
	if err != nil {
		return fmt.Errorf("Failed to determine database version: %w", err)
	}
	dbconn.Version = version
	return nil
}

/*
 * utilityModeConnStr appends the utility-mode GUC to connStr.  The GUC name
 * differs between major versions (gp_role on newer servers, gp_session_role
 * on older ones) and we don't get the database version until after the
 * connection is established, so we have to try one and see whether it works.
 *
 * gp_session_role is probed first because it is settable by any user on the
 * servers that still have it; gp_role on those same servers is restricted to
 * superusers, so probing it first would fail outright for everyone else.
 */
func (dbconn *DBConn) utilityModeConnStr(connStr string) (string, error) {
	roleConnStr := connStr + " gp_role=utility"
	sessionRoleConnStr := connStr + " gp_session_role=utility"

	utilConn, err := dbconn.Driver.Connect("pgx", sessionRoleConnStr)
	if utilConn != nil {
		_ = utilConn.Close()
	}
	if err == nil {
		return sessionRoleConnStr, nil
	}
	if strings.Contains(err.Error(), `unrecognized configuration parameter "gp_session_role"`) {
		return roleConnStr, nil
	}
	return "", dbconn.handleConnectionError(err)
}

func (dbconn *DBConn) MustConnectInUtilityMode(numConns int) {
	err := dbconn.Connect(numConns, true)
	gplog.FatalOnError(err)
}

func (dbconn *DBConn) ConnectInUtilityMode(numConns int) error {
	return dbconn.Connect(numConns, true)
}

func (dbconn *DBConn) handleConnectionError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlstateUndefinedObject:
			// The server raises 42704 both for a nonexistent role and for an
			// unrecognized configuration parameter, so anchor on the full
			// message shape. Loose substring checks on "role" plus the
			// username would misdiagnose a user named "gp" against
			// `unrecognized configuration parameter "gp_session_role"`.
			if strings.Contains(pgErr.Message, fmt.Sprintf(`role "%s" does not exist`, dbconn.User)) {
				return fmt.Errorf(`Role "%s" does not exist on %s:%d, exiting`, dbconn.User, dbconn.Host, dbconn.Port)
			}
		case sqlstateUndefinedDatabase:
			return fmt.Errorf(`Database "%s" does not exist on %s:%d, exiting`, dbconn.DBName, dbconn.Host, dbconn.Port)
		}
	}
	if strings.Contains(err.Error(), "connection refused") {
		return fmt.Errorf(`could not connect to server: Connection refused
	Is the server running on host "%s" and accepting
	TCP/IP connections on port %d?`, dbconn.Host, dbconn.Port)
	}
	return fmt.Errorf("%v (%s:%d)", err, dbconn.Host, dbconn.Port)
}

/*
 * Wrapper functions for built-in database/sql functionality; they will
 * automatically execute the query as part of an existing transaction if one is
 * in progress, to ensure that successive queries occur in one transaction without
 * requiring that to be ensured at the call site.
 */

func (dbconn *DBConn) Exec(query string, whichConn ...int) (sql.Result, error) {
	connNum := dbconn.ValidateConnNum(whichConn...)
	if dbconn.Tx[connNum] != nil {
		return dbconn.Tx[connNum].Exec(query)
	}
	return dbconn.ConnPool[connNum].Exec(query)
}

func (dbconn *DBConn) MustExec(query string, whichConn ...int) {
	_, err := dbconn.Exec(query, whichConn...)
	gplog.FatalOnError(err)
}

func (dbconn *DBConn) ExecContext(queryContext context.Context, query string, whichConn ...int) (sql.Result, error) {
	connNum := dbconn.ValidateConnNum(whichConn...)
	if dbconn.Tx[connNum] != nil {
		return dbconn.Tx[connNum].ExecContext(queryContext, query)
	}
	return dbconn.ConnPool[connNum].ExecContext(queryContext, query)
}

func (dbconn *DBConn) MustExecContext(queryContext context.Context, query string, whichConn ...int) {
	_, err := dbconn.ExecContext(queryContext, query, whichConn...)
	gplog.FatalOnError(err)
}

/*
 * query is the single funnel every read path goes through, so that transaction
 * affinity and context propagation are handled in one place.
 */
func (dbconn *DBConn) query(ctx context.Context, connNum int, query string, args ...interface{}) (*sql.Rows, error) {
	if dbconn.Tx[connNum] != nil {
		return dbconn.Tx[connNum].QueryContext(ctx, query, args...)
	}
	return dbconn.ConnPool[connNum].QueryContext(ctx, query, args...)
}

/*
 * Get scans a single row into destination, which may be a pointer to a struct
 * (columns are matched to fields by `db` tag, falling back to the lowercased
 * field name) or a pointer to a scalar for single-column queries.
 *
 * A query returning no rows yields sql.ErrNoRows and one returning more than
 * one row yields ErrMultipleRows; Get is for queries that are single-row by
 * construction, so pluralizing them is reported rather than silently ignored.
 */
func (dbconn *DBConn) Get(destination interface{}, query string, whichConn ...int) error {
	connNum := dbconn.ValidateConnNum(whichConn...)
	return dbconn.getContext(context.Background(), connNum, destination, query)
}

func (dbconn *DBConn) GetWithArgs(destination interface{}, query string, args ...interface{}) error {
	return dbconn.getContext(context.Background(), 0, destination, query, args...)
}

func (dbconn *DBConn) getContext(ctx context.Context, connNum int, destination interface{}, query string, args ...interface{}) error {
	rows, err := dbconn.query(ctx, connNum, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanOne(destination, rows)
}

/*
 * Select scans every row into destination, which must be a pointer to a slice.
 * The element type may be a struct (mapped by `db` tag) or a scalar for
 * single-column queries.
 */
func (dbconn *DBConn) Select(destination interface{}, query string, whichConn ...int) error {
	connNum := dbconn.ValidateConnNum(whichConn...)
	return dbconn.selectContext(context.Background(), connNum, destination, query)
}

func (dbconn *DBConn) SelectWithArgs(destination interface{}, query string, args ...interface{}) error {
	return dbconn.selectContext(context.Background(), 0, destination, query, args...)
}

func (dbconn *DBConn) SelectContext(ctx context.Context, destination interface{}, query string, whichConn ...int) error {
	connNum := dbconn.ValidateConnNum(whichConn...)
	return dbconn.selectContext(ctx, connNum, destination, query)
}

func (dbconn *DBConn) selectContext(ctx context.Context, connNum int, destination interface{}, query string, args ...interface{}) error {
	rows, err := dbconn.query(ctx, connNum, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAll(destination, rows)
}

/*
 * The Query functions hand the caller the raw *sql.Rows; the caller owns
 * closing them.
 */

func (dbconn *DBConn) Query(query string, whichConn ...int) (*sql.Rows, error) {
	connNum := dbconn.ValidateConnNum(whichConn...)
	return dbconn.query(context.Background(), connNum, query)
}

func (dbconn *DBConn) QueryWithArgs(query string, args ...interface{}) (*sql.Rows, error) {
	return dbconn.query(context.Background(), 0, query, args...)
}

func (dbconn *DBConn) QueryContext(ctx context.Context, query string, whichConn ...int) (*sql.Rows, error) {
	connNum := dbconn.ValidateConnNum(whichConn...)
	return dbconn.query(ctx, connNum, query)
}

/*
 * Ensure there isn't a mismatch between the connection pool size and number of
 * jobs, and default to using the first connection if no number is given.
 */
func (dbconn *DBConn) ValidateConnNum(whichConn ...int) int {
	if len(whichConn) == 0 {
		return 0
	}
	if len(whichConn) != 1 {
		gplog.Fatal(errors.New("At most one connection number may be specified for a given connection"), "")
	}
	if whichConn[0] < 0 || whichConn[0] >= dbconn.NumConns {
		gplog.Fatal(fmt.Errorf("Invalid connection number: %d", whichConn[0]), "")
	}
	return whichConn[0]
}

/*
 * Other useful/helper functions involving DBConn
 */

func EscapeConnectionParam(param string) string {
	param = strings.ReplaceAll(param, `\`, `\\`)
	param = strings.ReplaceAll(param, `'`, `\'`)
	return param
}

/*
 * This is a convenience function for Select() when we're selecting a single
 * string that may be NULL or not exist.  We can't use Get() because that
 * requires exactly one row and will error if no rows are returned, even if
 * using a sql.NullString.
 *
 * Unlike Get, SelectString treats an empty result set as the empty string, but
 * it still reports more than one row rather than silently returning the first,
 * since callers reach for it when exactly one row is expected.  Use
 * SelectStringSlice for genuinely multi-row queries.
 */
func MustSelectString(connection *DBConn, query string, whichConn ...int) string {
	str, err := SelectString(connection, query, whichConn...)
	gplog.FatalOnError(err)
	return str
}

func SelectString(connection *DBConn, query string, whichConn ...int) (string, error) {
	connNum := connection.ValidateConnNum(whichConn...)
	rows, err := connection.query(context.Background(), connNum, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if cols, _ := rows.Columns(); len(cols) > 1 {
		return "", fmt.Errorf("Too many columns returned from query: got %d columns, expected 1 column", len(cols))
	}
	if !rows.Next() {
		return "", rows.Err()
	}
	var result sql.NullString
	if err := rows.Scan(&result); err != nil {
		return "", err
	}
	if rows.Next() {
		return "", errors.New("Too many rows returned from query: expected at most 1 row")
	}
	return result.String, rows.Err()
}

/*
 * This is a convenience function for Select() when we're selecting a single
 * column of strings that may be NULL.  Select requires defining a struct for
 * each call, and this function scans the column directly instead, to avoid
 * needing to "SELECT [column] AS [struct field]" with a generic struct or the
 * like.
 *
 * It also gives a nicer error message in the event that a query is called with
 * multiple columns, where using a generic struct gives an opaque "missing
 * destination name" error.
 */
func MustSelectStringSlice(connection *DBConn, query string, whichConn ...int) []string {
	str, err := SelectStringSlice(connection, query, whichConn...)
	gplog.FatalOnError(err)
	return str
}

func SelectStringSlice(connection *DBConn, query string, whichConn ...int) ([]string, error) {
	connNum := connection.ValidateConnNum(whichConn...)
	rows, err := connection.query(context.Background(), connNum, query)
	if err != nil {
		return []string{}, err
	}
	defer rows.Close()
	if cols, _ := rows.Columns(); len(cols) > 1 {
		return []string{}, fmt.Errorf("Too many columns returned from query: got %d columns, expected 1 column", len(cols))
	}
	retval := make([]string, 0)
	for rows.Next() {
		var result sql.NullString
		if err := rows.Scan(&result); err != nil {
			return []string{}, err
		}
		retval = append(retval, result.String)
	}
	if err := rows.Err(); err != nil {
		return []string{}, err
	}
	return retval, nil
}

/*
 * The below are convenience functions for selecting one or more ints that may
 * be NULL or may not exist, with the same functionality (and the same rationale)
 * as SelectString and SelectStringSlice; see the comments for those functions,
 * above, for more details.
 */
func MustSelectInt(connection *DBConn, query string, whichConn ...int) int {
	str, err := SelectInt(connection, query, whichConn...)
	gplog.FatalOnError(err)
	return str
}

func SelectInt(connection *DBConn, query string, whichConn ...int) (int, error) {
	connNum := connection.ValidateConnNum(whichConn...)
	rows, err := connection.query(context.Background(), connNum, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if cols, _ := rows.Columns(); len(cols) > 1 {
		return 0, fmt.Errorf("Too many columns returned from query: got %d columns, expected 1 column", len(cols))
	}
	if !rows.Next() {
		return 0, rows.Err()
	}
	var result sql.NullInt32
	if err := rows.Scan(&result); err != nil {
		return 0, err
	}
	if rows.Next() {
		return 0, errors.New("Too many rows returned from query: expected at most 1 row")
	}
	return int(result.Int32), rows.Err()
}

func MustSelectIntSlice(connection *DBConn, query string, whichConn ...int) []int {
	str, err := SelectIntSlice(connection, query, whichConn...)
	gplog.FatalOnError(err)
	return str
}

func SelectIntSlice(connection *DBConn, query string, whichConn ...int) ([]int, error) {
	connNum := connection.ValidateConnNum(whichConn...)
	rows, err := connection.query(context.Background(), connNum, query)
	if err != nil {
		return []int{}, err
	}
	defer rows.Close()
	if cols, _ := rows.Columns(); len(cols) > 1 {
		return []int{}, fmt.Errorf("Too many columns returned from query: got %d columns, expected 1 column", len(cols))
	}
	retval := make([]int, 0)
	for rows.Next() {
		var result sql.NullInt32
		if err := rows.Scan(&result); err != nil {
			return []int{}, err
		}
		retval = append(retval, int(result.Int32))
	}
	if err := rows.Err(); err != nil {
		return []int{}, err
	}
	return retval, nil
}
