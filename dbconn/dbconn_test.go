package dbconn_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/warehouse-pg/common-go-libs/dbconn"
	"github.com/warehouse-pg/common-go-libs/operating"
	"github.com/warehouse-pg/common-go-libs/testhelper"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	connection *dbconn.DBConn
	mock       sqlmock.Sqlmock
)

func ExpectBegin(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
}

func TestDBConn(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "dbconn tests")
}

var _ = BeforeSuite(func() {
	testhelper.SetupTestEnvironment()
})

var _ = BeforeEach(func() {
	connection, mock = testhelper.CreateAndConnectMockDB(1)
})

var _ = AfterEach(func() {
	if connection != nil {
		connection.Close()
	}
})

var _ = Describe("dbconn/dbconn tests", func() {
	BeforeEach(func() {
		operating.System.Now = func() time.Time { return time.Date(2017, time.January, 1, 1, 1, 1, 1, time.Local) }
	})
	Describe("NewDBConn", func() {
		It("gets the DBName from the dbname flag if it is set", func() {
			connection = dbconn.NewDBConnFromEnvironment("testdb")
			Expect(connection.DBName).To(Equal("testdb"))
		})
		It("gets the DB info", func() {
			connection = dbconn.NewDBConn("testdb", "testuser", "mars", 1234)
			Expect(connection.DBName).To(Equal("testdb"))
			Expect(connection.User).To(Equal("testuser"))
			Expect(connection.Host).To(Equal("mars"))
			Expect(connection.Port).To(Equal(1234))
		})
		It("fails if no database is given with the dbname flag", func() {
			defer testhelper.ShouldPanicWithMessage("No database provided")
			connection = dbconn.NewDBConnFromEnvironment("")
		})
		It("fails if username is an empty string", func() {
			defer testhelper.ShouldPanicWithMessage("No username provided")
			connection = dbconn.NewDBConn("testdb", "", "mars", 1234)
		})
		It("fails if host is an empty string", func() {
			defer testhelper.ShouldPanicWithMessage("No host provided")
			connection = dbconn.NewDBConn("testdb", "testuser", "", 1234)
		})
	})
	Describe("DBConn.MustConnect", func() {
		var mockdb *sql.DB
		BeforeEach(func() {
			connection, mock = testhelper.CreateMockDBConn()
			testhelper.ExpectVersionQuery(mock, "5.1.0")
		})
		It("makes a single connection successfully if the database exists", func() {
			connection.MustConnect(1)
			Expect(connection.DBName).To(Equal("testdb"))
			Expect(connection.NumConns).To(Equal(1))
			Expect(len(connection.ConnPool)).To(Equal(1))
			Expect(len(connection.Tx)).To(Equal(1))
		})
		It("makes multiple connections successfully if the database exists", func() {
			connection.MustConnect(3)
			Expect(connection.DBName).To(Equal("testdb"))
			Expect(connection.NumConns).To(Equal(3))
			Expect(len(connection.ConnPool)).To(Equal(3))
			Expect(len(connection.Tx)).To(Equal(3))
		})
		It("does not connect if the database exists but the connection is refused", func() {
			connection.Driver = &testhelper.TestDriver{ErrToReturn: fmt.Errorf("dial tcp 127.0.0.1:5432: connect: connection refused"), DB: mockdb, User: "testrole"}
			defer testhelper.ShouldPanicWithMessage(`could not connect to server: Connection refused`)
			connection.MustConnect(1)
		})
		It("fails if an invalid number of connections is given", func() {
			defer testhelper.ShouldPanicWithMessage("Must specify a connection pool size that is a positive integer")
			connection.MustConnect(0)
		})
		It("fails if the database does not exist", func() {
			connection.Driver = &testhelper.TestDriver{ErrToReturn: &pgconn.PgError{Code: "3D000", Message: `database "testdb" does not exist`}, DB: mockdb, DBName: "testdb", User: "testrole"}
			Expect(connection.DBName).To(Equal("testdb"))
			defer testhelper.ShouldPanicWithMessage("Database \"testdb\" does not exist on testhost:5432, exiting")
			connection.MustConnect(1)
		})
		It("fails if the role does not exist", func() {
			oldPgUser := os.Getenv("PGUSER")
			os.Setenv("PGUSER", "nonexistent")
			defer os.Setenv("PGUSER", oldPgUser)

			connection = dbconn.NewDBConnFromEnvironment("testdb")
			connection.Driver = &testhelper.TestDriver{ErrToReturn: &pgconn.PgError{Code: "42704", Message: `role "nonexistent" does not exist`}, DB: mockdb, DBName: "testdb", User: "nonexistent"}
			Expect(connection.User).To(Equal("nonexistent"))
			expectedStr := fmt.Sprintf("Role \"nonexistent\" does not exist on %s:%d, exiting", connection.Host, connection.Port)
			defer testhelper.ShouldPanicWithMessage(expectedStr)
			connection.MustConnect(1)
		})
	})
	Describe("DBConn.Connect", func() {
		It("can connect in utility mode where gp_session_role is accepted", func() {
			// The leading nil makes TestDriver return (nil, nil) for the probe
			// call. That matters because TestDriver hands out one shared
			// *sql.DB and the probe closes whatever it is given; in production
			// each Driver.Connect call returns a distinct handle.
			connection, mock = testhelper.CreateMockDBConn(nil)
			testhelper.ExpectVersionQuery(mock, "6.0.0")

			err := connection.Connect(1, true)
			Expect(err).ToNot(HaveOccurred())
		})
		It("falls back to gp_role where gp_session_role no longer exists", func() {
			connection, mock = testhelper.CreateMockDBConn(fmt.Errorf(`ERROR: unrecognized configuration parameter "gp_session_role" (SQLSTATE 42704)`))
			testhelper.ExpectVersionQuery(mock, "7.0.0")

			err := connection.Connect(1, true)
			Expect(err).ToNot(HaveOccurred())
		})
		It("does not misreport an unrecognized-parameter error as a missing role", func() {
			// The message `unrecognized configuration parameter
			// "gp_session_role"` contains both "role" and "gp" as substrings,
			// so a user named gp must not be diagnosed as a nonexistent role.
			connection, _ = testhelper.CreateMockDBConn(&pgconn.PgError{
				Code:    "42704",
				Message: `unrecognized configuration parameter "gp_session_role"`,
			})
			connection.User = "gp"

			err := connection.Connect(1)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).ToNot(ContainSubstring("does not exist"))
			Expect(err.Error()).To(ContainSubstring("unrecognized configuration parameter"))
		})
		It("passes an error message on if a utility mode connection fails", func() {
			connection, mock = testhelper.CreateMockDBConn(&pgconn.PgError{
				Code:    "3D000",
				Message: `database "testdb" does not exist`,
			})
			testhelper.ExpectVersionQuery(mock, "6.0.0")

			Expect(connection.DBName).To(Equal("testdb"))
			err := connection.Connect(1, true)
			Expect(err.Error()).To(Equal(`Database "testdb" does not exist on testhost:5432, exiting`))
		})
	})
	Describe("DBConn.Close", func() {
		BeforeEach(func() {
			connection, mock = testhelper.CreateMockDBConn()
			testhelper.ExpectVersionQuery(mock, "5.1.0")
		})
		It("successfully closes a dbconn with a single open connection", func() {
			connection.MustConnect(1)
			Expect(connection.NumConns).To(Equal(1))
			Expect(len(connection.ConnPool)).To(Equal(1))
			connection.Close()
			Expect(connection.NumConns).To(Equal(0))
			Expect(connection.ConnPool).To(BeNil())
		})
		It("successfully closes a dbconn with multiple open connections", func() {
			connection.MustConnect(3)
			Expect(connection.NumConns).To(Equal(3))
			Expect(len(connection.ConnPool)).To(Equal(3))
			Expect(len(connection.Tx)).To(Equal(3))
			connection.Close()
			Expect(connection.NumConns).To(Equal(0))
			Expect(connection.ConnPool).To(BeNil())
			Expect(connection.Tx).To(BeNil())
		})
		It("does nothing if there are no open connections", func() {
			connection.MustConnect(3)
			connection.Close()
			Expect(connection.NumConns).To(Equal(0))
			Expect(connection.ConnPool).To(BeNil())
			Expect(connection.Tx).To(BeNil())
			connection.Close()
			Expect(connection.NumConns).To(Equal(0))
			Expect(connection.ConnPool).To(BeNil())
			Expect(connection.Tx).To(BeNil())
		})
	})
	Describe("DBConn.Exec", func() {
		It("executes an INSERT outside of a transaction", func() {
			fakeResult := testhelper.TestResult{Rows: 1}
			mock.ExpectExec("INSERT (.*)").WillReturnResult(fakeResult)

			res, err := connection.Exec("INSERT INTO pg_tables VALUES ('schema', 'table')")
			Expect(err).ToNot(HaveOccurred())
			rowsReturned, err := res.RowsAffected()
			Expect(rowsReturned).To(Equal(int64(1)))
		})
		It("executes an INSERT in a transaction", func() {
			fakeResult := testhelper.TestResult{Rows: 1}
			ExpectBegin(mock)
			mock.ExpectExec("INSERT (.*)").WillReturnResult(fakeResult)
			mock.ExpectCommit()

			connection.MustBegin()
			res, err := connection.Exec("INSERT INTO pg_tables VALUES ('schema', 'table')")
			connection.MustCommit()
			Expect(err).ToNot(HaveOccurred())
			rowsReturned, err := res.RowsAffected()
			Expect(rowsReturned).To(Equal(int64(1)))
		})
	})
	Describe("DBConn.ExecContext", func() {
		It("executes an INSERT outside of a transaction", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeResult := testhelper.TestResult{Rows: 1}
			mock.ExpectExec("INSERT (.*)").WillReturnResult(fakeResult)

			res, err := connection.ExecContext(ctx, "INSERT INTO pg_tables VALUES ('schema', 'table')")
			Expect(err).ToNot(HaveOccurred())
			rowsReturned, err := res.RowsAffected()
			Expect(rowsReturned).To(Equal(int64(1)))
		})
		It("executes an INSERT in a transaction", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeResult := testhelper.TestResult{Rows: 1}
			ExpectBegin(mock)
			mock.ExpectExec("INSERT (.*)").WillReturnResult(fakeResult)
			mock.ExpectCommit()

			connection.MustBegin()
			res, err := connection.ExecContext(ctx, "INSERT INTO pg_tables VALUES ('schema', 'table')")
			connection.MustCommit()
			Expect(err).ToNot(HaveOccurred())
			rowsReturned, err := res.RowsAffected()
			Expect(rowsReturned).To(Equal(int64(1)))
		})
	})
	Describe("DBConn.Get", func() {
		It("executes a GET outside of a transaction", func() {
			two_col_single_row := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_single_row)

			testRecord := struct {
				Schemaname string
				Tablename  string
			}{}

			err := connection.Get(&testRecord, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname")

			Expect(err).ToNot(HaveOccurred())
			Expect(testRecord.Schemaname).To(Equal("schema1"))
			Expect(testRecord.Tablename).To(Equal("table1"))
		})
		It("executes a GET with argument outside of a transaction", func() {
			arg1 := "table1"
			arg2 := "table2"
			two_col_single_row := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1")
			mock.ExpectQuery("SELECT (.*)").WithArgs(arg1, arg2).WillReturnRows(two_col_single_row)

			testRecord := struct {
				Schemaname string
				Tablename  string
			}{}

			err := connection.GetWithArgs(&testRecord, "SELECT schemaname, tablename FROM two_columns WHERE tablename=$1 OR tablename=$2", arg1, arg2)
			Expect(err).ToNot(HaveOccurred())
			Expect(testRecord.Schemaname).To(Equal("schema1"))
			Expect(testRecord.Tablename).To(Equal("table1"))
		})
		It("executes a GET in a transaction", func() {
			two_col_single_row := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1")
			ExpectBegin(mock)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_single_row)
			mock.ExpectCommit()

			testRecord := struct {
				Schemaname string
				Tablename  string
			}{}

			connection.MustBegin()
			err := connection.Get(&testRecord, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname")
			connection.MustCommit()
			Expect(err).ToNot(HaveOccurred())
			Expect(testRecord.Schemaname).To(Equal("schema1"))
			Expect(testRecord.Tablename).To(Equal("table1"))
		})
		It("returns ErrMultipleRows when the query returns more than one row", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			testRecord := struct {
				Schemaname string
				Tablename  string
			}{}

			err := connection.Get(&testRecord, "SELECT schemaname, tablename FROM two_columns")
			Expect(err).To(MatchError(dbconn.ErrMultipleRows))
		})
		It("returns sql.ErrNoRows when the query returns no rows", func() {
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(sqlmock.NewRows([]string{"schemaname", "tablename"}))

			testRecord := struct {
				Schemaname string
				Tablename  string
			}{}

			err := connection.Get(&testRecord, "SELECT schemaname, tablename FROM two_columns")
			Expect(err).To(MatchError(sql.ErrNoRows))
		})
	})
	Describe("DBConn.Select", func() {
		It("executes a SELECT outside of a transaction", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			testSlice := make([]struct {
				Schemaname string
				Tablename  string
			}, 0)

			err := connection.Select(&testSlice, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname LIMIT 2")

			Expect(err).ToNot(HaveOccurred())
			Expect(len(testSlice)).To(Equal(2))
			Expect(testSlice[0].Schemaname).To(Equal("schema1"))
			Expect(testSlice[0].Tablename).To(Equal("table1"))
			Expect(testSlice[1].Schemaname).To(Equal("schema2"))
			Expect(testSlice[1].Tablename).To(Equal("table2"))
		})
		It("executes a SELECT with argument outside of a transaction", func() {
			arg1 := "table1"
			arg2 := "table2"
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WithArgs(arg1, arg2).WillReturnRows(two_col_rows)

			testSlice := make([]struct {
				Schemaname string
				Tablename  string
			}, 0)

			err := connection.SelectWithArgs(&testSlice, "SELECT schemaname, tablename FROM two_columns WHERE tablename=$1 OR tablename=$2", arg1, arg2)

			Expect(err).ToNot(HaveOccurred())
			Expect(len(testSlice)).To(Equal(2))
			Expect(testSlice[0].Schemaname).To(Equal("schema1"))
			Expect(testSlice[0].Tablename).To(Equal("table1"))
			Expect(testSlice[1].Schemaname).To(Equal("schema2"))
			Expect(testSlice[1].Tablename).To(Equal("table2"))
		})
		It("executes a SELECT in a transaction", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			ExpectBegin(mock)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)
			mock.ExpectCommit()

			testSlice := make([]struct {
				Schemaname string
				Tablename  string
			}, 0)

			connection.MustBegin()
			err := connection.Select(&testSlice, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname LIMIT 2")
			connection.MustCommit()

			Expect(err).ToNot(HaveOccurred())
			Expect(len(testSlice)).To(Equal(2))
			Expect(testSlice[0].Schemaname).To(Equal("schema1"))
			Expect(testSlice[0].Tablename).To(Equal("table1"))
			Expect(testSlice[1].Schemaname).To(Equal("schema2"))
			Expect(testSlice[1].Tablename).To(Equal("table2"))
		})
	})
	Describe("DBConn.SelectContext", func() {
		It("executes a SELECT outside of a transaction", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			testSlice := make([]struct {
				Schemaname string
				Tablename  string
			}, 0)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := connection.SelectContext(ctx, &testSlice, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname LIMIT 2")

			Expect(err).ToNot(HaveOccurred())
			Expect(len(testSlice)).To(Equal(2))
			Expect(testSlice[0].Schemaname).To(Equal("schema1"))
			Expect(testSlice[0].Tablename).To(Equal("table1"))
			Expect(testSlice[1].Schemaname).To(Equal("schema2"))
			Expect(testSlice[1].Tablename).To(Equal("table2"))
		})
		It("errors out when the context is cancelled", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			testSlice := make([]struct {
				Schemaname string
				Tablename  string
			}, 0)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := connection.SelectContext(ctx, &testSlice, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname LIMIT 2")

			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(context.Canceled))
		})
	})
	Describe("DBConn.QueryContext", func() {
		It("executes a QUERY and returns the correct rows", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			type testSlice struct {
				Schemaname string
				Tablename  string
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rows, err := connection.QueryContext(ctx, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname LIMIT 2")
			defer rows.Close()

			columns, _ := rows.Columns()

			var result []testSlice
			for rows.Next() {
				var row testSlice
				Expect(rows.Scan(&row.Schemaname, &row.Tablename)).To(Succeed())
				result = append(result, row)
			}

			Expect(err).ToNot(HaveOccurred())
			Expect(len(result)).To(Equal(2))
			Expect(columns).To(Equal([]string{"schemaname", "tablename"}))
			Expect(result[0].Schemaname).To(Equal("schema1"))
			Expect(result[0].Tablename).To(Equal("table1"))
			Expect(result[1].Schemaname).To(Equal("schema2"))
			Expect(result[1].Tablename).To(Equal("table2"))
		})
		It("errors out when the context is cancelled", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err := connection.QueryContext(ctx, "SELECT schemaname, tablename FROM two_columns ORDER BY schemaname LIMIT 2")

			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(context.Canceled))
		})
	})
	Describe("DBConn.MustBegin", func() {
		It("successfully executes a BEGIN outside a transaction", func() {
			ExpectBegin(mock)
			connection.MustBegin()
			Expect(connection.Tx).To(Not(BeNil()))
		})
		It("panics if it executes a BEGIN in a transaction", func() {
			ExpectBegin(mock)
			connection.MustBegin()
			defer testhelper.ShouldPanicWithMessage("Cannot begin transaction; there is already a transaction in progress")
			connection.MustBegin()
		})
	})
	Describe("DBConn.MustCommit", func() {
		It("successfully executes a COMMIT in a transaction", func() {
			ExpectBegin(mock)
			mock.ExpectCommit()
			connection.MustBegin()
			connection.MustCommit()
			Expect(connection.Tx[0]).To(BeNil())
		})
		It("panics if it executes a COMMIT outside a transaction", func() {
			defer testhelper.ShouldPanicWithMessage("Cannot commit transaction; there is no transaction in progress")
			connection.MustCommit()
		})
	})
	Describe("DBConn.Rollback", func() {
		It("successfully executes a ROLLBACK in a transaction", func() {
			ExpectBegin(mock)
			mock.ExpectRollback()
			connection.MustBegin()
			err := connection.Rollback()
			Expect(err).ToNot(HaveOccurred())
			Expect(connection.Tx[0]).To(BeNil())
		})
		It("returns an error if it executes a ROLLBACK outside a transaction", func() {
			err := connection.Rollback()
			Expect(err).To(MatchError("Cannot rollback transaction; there is no transaction in progress"))
		})
		It("panics via MustRollback outside a transaction", func() {
			defer testhelper.ShouldPanicWithMessage("Cannot rollback transaction; there is no transaction in progress")
			connection.MustRollback()
		})
	})
	Describe("DBConn.Query", func() {
		// Query returns *sql.Rows rather than sqlx's *sqlx.Rows; the caller owns
		// closing them.
		It("returns rows the caller scans directly", func() {
			two_col_rows := sqlmock.NewRows([]string{"schemaname", "tablename"}).
				AddRow("schema1", "table1").
				AddRow("schema2", "table2")
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(two_col_rows)

			rows, err := connection.Query("SELECT schemaname, tablename FROM two_columns")
			Expect(err).ToNot(HaveOccurred())
			defer rows.Close()

			var schemas []string
			for rows.Next() {
				var schema, table string
				Expect(rows.Scan(&schema, &table)).To(Succeed())
				schemas = append(schemas, schema)
			}
			Expect(rows.Err()).ToNot(HaveOccurred())
			Expect(schemas).To(Equal([]string{"schema1", "schema2"}))
		})
		It("passes arguments through with QueryWithArgs", func() {
			one_col_row := sqlmock.NewRows([]string{"tablename"}).AddRow("table1")
			mock.ExpectQuery("SELECT (.*)").WithArgs("table1").WillReturnRows(one_col_row)

			rows, err := connection.QueryWithArgs("SELECT tablename FROM two_columns WHERE tablename=$1", "table1")
			Expect(err).ToNot(HaveOccurred())
			defer rows.Close()

			Expect(rows.Next()).To(BeTrue())
			var table string
			Expect(rows.Scan(&table)).To(Succeed())
			Expect(table).To(Equal("table1"))
		})
	})
	Describe("DBConn.ConnectInUtilityMode", func() {
		It("connects with gp_role on the first probe", func() {
			// The leading nil makes TestDriver return (nil, nil) for the probe
			// call. That matters because TestDriver hands out one shared
			// *sql.DB, and the probe closes whatever it is given - in
			// production each Driver.Connect call returns a distinct handle.
			connection, mock = testhelper.CreateMockDBConn(nil)
			testhelper.ExpectVersionQuery(mock, "7.0.0")

			err := connection.ConnectInUtilityMode(1)
			Expect(err).ToNot(HaveOccurred())
			Expect(connection.NumConns).To(Equal(1))
		})
		It("panics via MustConnectInUtilityMode when the connection fails", func() {
			connection, mock = testhelper.CreateMockDBConn(&pgconn.PgError{
				Code:    "3D000",
				Message: `database "testdb" does not exist`,
			})
			testhelper.ExpectVersionQuery(mock, "7.0.0")

			defer testhelper.ShouldPanicWithMessage(`Database "testdb" does not exist on testhost:5432, exiting`)
			connection.MustConnectInUtilityMode(1)
		})
	})
	Describe("GPDBDriver.Connect", func() {
		// sql.Open is lazy, so the real driver pings to make a bad connection
		// surface here rather than on the first query.
		It("returns an error rather than a handle when the server is unreachable", func() {
			driver := &dbconn.GPDBDriver{}
			db, err := driver.Connect("pgx", "host=127.0.0.1 port=1 dbname=nonexistent connect_timeout=1 sslmode=disable")
			Expect(err).To(HaveOccurred())
			Expect(db).To(BeNil())
		})
		It("returns an error for an unregistered driver name", func() {
			driver := &dbconn.GPDBDriver{}
			db, err := driver.Connect("no-such-driver", "")
			Expect(err).To(HaveOccurred())
			Expect(db).To(BeNil())
		})
	})
	Describe("Dbconn.ValidateConnNum", func() {
		BeforeEach(func() {
			connection, mock = testhelper.CreateMockDBConn()
			testhelper.ExpectVersionQuery(mock, "5.1.0")
			connection.MustConnect(3)
		})
		It("returns the connection number if it is valid", func() {
			num := connection.ValidateConnNum(1)
			Expect(num).To(Equal(1))
		})
		It("defaults to 0 with no argument", func() {
			num := connection.ValidateConnNum()
			Expect(num).To(Equal(0))
		})
		It("panics if given multiple arguments", func() {
			defer testhelper.ShouldPanicWithMessage("At most one connection number may be specified for a given connection")
			connection.ValidateConnNum(1, 2)
		})
		It("panics if given a negative number", func() {
			defer testhelper.ShouldPanicWithMessage("Invalid connection number: -1")
			connection.ValidateConnNum(-1)
		})
		It("panics if given a number greater than NumConns", func() {
			defer testhelper.ShouldPanicWithMessage("Invalid connection number: 4")
			connection.ValidateConnNum(4)
		})
	})
	Describe("MustSelectString", func() {
		header := []string{"foo"}
		rowOne := []driver.Value{"one"}
		rowTwo := []driver.Value{"two"}
		headerExtraCol := []string{"foo", "bar"}
		rowExtraCol := []driver.Value{"one", "two"}

		It("returns a single string if the query selects a single string", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			result := dbconn.MustSelectString(connection, "SELECT foo FROM bar")
			Expect(result).To(Equal("one"))
		})
		It("returns an empty string if the query selects no strings", func() {
			fakeResult := sqlmock.NewRows(header)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			result := dbconn.MustSelectString(connection, "SELECT foo FROM bar")
			Expect(result).To(Equal(""))
		})
		It("panics if the query selects multiple rows", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...).AddRow(rowTwo...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			defer testhelper.ShouldPanicWithMessage("Too many rows returned from query: expected at most 1 row")
			dbconn.MustSelectString(connection, "SELECT foo FROM bar")
		})
		It("panics if the query selects multiple columns", func() {
			fakeResult := sqlmock.NewRows(headerExtraCol).AddRow(rowExtraCol...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			defer testhelper.ShouldPanicWithMessage("Too many columns returned from query: got 2 columns, expected 1 column")
			dbconn.MustSelectString(connection, "SELECT foo FROM bar")
		})
	})
	Describe("MustSelectStringSlice", func() {
		header := []string{"foo"}
		rowOne := []driver.Value{"one"}
		rowTwo := []driver.Value{"two"}
		headerExtraCol := []string{"foo", "bar"}
		rowExtraCol := []driver.Value{"one", "two"}

		It("returns a slice containing a single string if the query selects a single string", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			results := dbconn.MustSelectStringSlice(connection, "SELECT foo FROM bar")
			Expect(len(results)).To(Equal(1))
			Expect(results[0]).To(Equal("one"))
		})
		It("returns an empty slice if the query selects no strings", func() {
			fakeResult := sqlmock.NewRows(header)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			results := dbconn.MustSelectStringSlice(connection, "SELECT foo FROM bar")
			Expect(len(results)).To(Equal(0))
		})
		It("returns a slice containing multiple strings if the query selects multiple rows", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...).AddRow(rowTwo...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			results := dbconn.MustSelectStringSlice(connection, "SELECT foo FROM bar")
			Expect(len(results)).To(Equal(2))
			Expect(results[0]).To(Equal("one"))
			Expect(results[1]).To(Equal("two"))
		})
		It("panics if the query selects multiple columns", func() {
			fakeResult := sqlmock.NewRows(headerExtraCol).AddRow(rowExtraCol...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			defer testhelper.ShouldPanicWithMessage("Too many columns returned from query: got 2 columns, expected 1 column")
			dbconn.MustSelectString(connection, "SELECT foo FROM bar")
		})
	})
	Describe("MustSelectInt", func() {
		header := []string{"foo"}
		rowOne := []driver.Value{"1"}
		rowTwo := []driver.Value{"2"}
		headerExtraCol := []string{"foo", "bar"}
		rowExtraCol := []driver.Value{"1", "2"}

		It("returns a single int if the query selects a single int", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			result := dbconn.MustSelectInt(connection, "SELECT foo FROM bar")
			Expect(result).To(Equal(1))
		})
		It("returns 0 if the query selects no ints", func() {
			fakeResult := sqlmock.NewRows(header)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			result := dbconn.MustSelectInt(connection, "SELECT foo FROM bar")
			Expect(result).To(Equal(0))
		})
		It("panics if the query selects multiple rows", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...).AddRow(rowTwo...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			defer testhelper.ShouldPanicWithMessage("Too many rows returned from query: expected at most 1 row")
			dbconn.MustSelectInt(connection, "SELECT foo FROM bar")
		})
		It("panics if the query selects multiple columns", func() {
			fakeResult := sqlmock.NewRows(headerExtraCol).AddRow(rowExtraCol...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			defer testhelper.ShouldPanicWithMessage("Too many columns returned from query: got 2 columns, expected 1 column")
			dbconn.MustSelectInt(connection, "SELECT foo FROM bar")
		})
	})
	Describe("MustSelectIntSlice", func() {
		header := []string{"foo"}
		rowOne := []driver.Value{"1"}
		rowTwo := []driver.Value{"2"}
		headerExtraCol := []string{"foo", "bar"}
		rowExtraCol := []driver.Value{"1", "2"}

		It("returns a slice containing a single int if the query selects a single int", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			results := dbconn.MustSelectIntSlice(connection, "SELECT foo FROM bar")
			Expect(len(results)).To(Equal(1))
			Expect(results[0]).To(Equal(1))
		})
		It("returns an empty slice if the query selects no ints", func() {
			fakeResult := sqlmock.NewRows(header)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			results := dbconn.MustSelectIntSlice(connection, "SELECT foo FROM bar")
			Expect(len(results)).To(Equal(0))
		})
		It("returns a slice containing multiple ints if the query selects multiple rows", func() {
			fakeResult := sqlmock.NewRows(header).AddRow(rowOne...).AddRow(rowTwo...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			results := dbconn.MustSelectIntSlice(connection, "SELECT foo FROM bar")
			Expect(len(results)).To(Equal(2))
			Expect(results[0]).To(Equal(1))
			Expect(results[1]).To(Equal(2))
		})
		It("panics if the query selects multiple columns", func() {
			fakeResult := sqlmock.NewRows(headerExtraCol).AddRow(rowExtraCol...)
			mock.ExpectQuery("SELECT (.*)").WillReturnRows(fakeResult)
			defer testhelper.ShouldPanicWithMessage("Too many columns returned from query: got 2 columns, expected 1 column")
			dbconn.MustSelectInt(connection, "SELECT foo FROM bar")
		})
	})
})
