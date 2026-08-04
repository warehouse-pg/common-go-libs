package dbconn

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func openMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func query(t *testing.T, db *sql.DB) *sql.Rows {
	t.Helper()
	rows, err := db.Query("SELECT *")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return rows
}

// db: tag mapping wins over field name.
func TestScanAll_DBTagMapping(t *testing.T) {
	type Row struct {
		ID   int    `db:"row_id"`
		Name string `db:"row_name"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"row_id", "row_name"}).
			AddRow(1, "alice").
			AddRow(2, "bob"),
	)

	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	want := []Row{{1, "alice"}, {2, "bob"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v want %+v", got, want)
	}
}

// Missing db: tag falls back to lowercased field name.
func TestScanAll_LowercasedFieldName(t *testing.T) {
	type Row struct {
		Oid  uint32
		Name string
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"oid", "name"}).AddRow(uint32(42), "x"),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 || got[0].Oid != 42 || got[0].Name != "x" {
		t.Errorf("got %+v", got)
	}
}

// Embedded struct fields are reachable via the same flat column space.
func TestScanAll_EmbeddedStruct(t *testing.T) {
	type Base struct {
		ID int `db:"id"`
	}
	type Row struct {
		Base
		Name string `db:"name"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id", "name"}).AddRow(7, "deep"),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 || got[0].Name != "deep" {
		t.Errorf("got %+v", got)
	}
}

// A result column with no destination field is an error, the way sqlx's
// "missing destination name" was: a db-tag typo or a renamed column must
// fail the query, not silently produce zero values.
func TestScanAll_UnknownColumnErrors(t *testing.T) {
	type Row struct {
		ID int `db:"id"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id", "extra"}).AddRow(1, "boom"),
	)
	var got []Row
	err := scanAll(&got, query(t, db))
	if err == nil || !strings.Contains(err.Error(), `"extra"`) {
		t.Fatalf("want missing-destination error naming extra, got err=%v rows=%+v", err, got)
	}
}

// Same contract through the single-row path: a tag typo means the only
// column no longer matches, and must error rather than zero-fill.
func TestScanOne_UnknownColumnErrors(t *testing.T) {
	type Row struct {
		Name string `db:"name"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"relname"}).AddRow("mistagged"),
	)
	var got Row
	err := scanOne(&got, query(t, db))
	if err == nil || !strings.Contains(err.Error(), `"relname"`) {
		t.Fatalf("want missing-destination error naming relname, got err=%v row=%+v", err, got)
	}
}

// Structs with no mappable fields (time.Time - all fields unexported) are
// handed to the driver directly instead of being silently zero-filled by
// the field-mapping path.
func TestScanOne_TimeTime(t *testing.T) {
	db, mock := openMock(t)
	want := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"now"}).AddRow(want),
	)
	var ts time.Time
	if err := scanOne(&ts, query(t, db)); err != nil {
		t.Fatalf("scanOne: %v", err)
	}
	if !ts.Equal(want) {
		t.Errorf("got %v want %v", ts, want)
	}
}

// Embedded pointer structs are flattened like embedded values, with nil
// pointers along the path allocated rather than the columns being dropped.
func TestScanAll_EmbeddedPointerStruct(t *testing.T) {
	type Base struct {
		ID int `db:"id"`
	}
	type Row struct {
		*Base
		Extra string `db:"extra"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id", "extra"}).AddRow(7, "x"),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows: %+v", got)
	}
	if got[0].Base == nil {
		t.Fatal("embedded *Base was not allocated")
	}
	if got[0].ID != 7 || got[0].Extra != "x" {
		t.Errorf("got %+v (Base=%+v)", got[0], *got[0].Base)
	}
}

// Go's field promotion rules apply to name collisions: the outer field
// shadows the embedded one, so the column lands on the outer field.
func TestScanAll_ShadowedFieldOuterWins(t *testing.T) {
	type Base struct {
		Name string
	}
	type Row struct {
		Base
		Name string
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"name"}).AddRow("outer"),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows: %+v", got)
	}
	if got[0].Name != "outer" || got[0].Base.Name != "" {
		t.Errorf("outer=%q embedded=%q; want the outer field to win", got[0].Name, got[0].Base.Name)
	}
}

// Fields tagged db:"-" are skipped entirely.
func TestScanAll_SkipDashTag(t *testing.T) {
	type Row struct {
		ID  int `db:"id"`
		Sec int `db:"-"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(99),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != 99 || got[0].Sec != 0 {
		t.Errorf("got %+v", got)
	}
}

// StringArray implements sql.Scanner; scanRowIntoStruct must hand the column
// value to that Scanner via rows.Scan rather than reflecting into it.
func TestScanAll_SQLScannerField(t *testing.T) {
	type Row struct {
		ID   int         `db:"id"`
		Tags StringArray `db:"tags"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id", "tags"}).AddRow(1, "{a,b,c}"),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows: %+v", got)
	}
	if got[0].ID != 1 || len(got[0].Tags) != 3 || got[0].Tags[0] != "a" {
		t.Errorf("got %+v", got)
	}
}

// sql.NullString fields work as a special case of sql.Scanner.
func TestScanAll_NullableField(t *testing.T) {
	type Row struct {
		Name sql.NullString `db:"name"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"name"}).AddRow(nil).AddRow("set"),
	)
	var got []Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 2 || got[0].Name.Valid || !got[1].Name.Valid || got[1].Name.String != "set" {
		t.Errorf("got %+v", got)
	}
}

// Slice of pointer to struct also works.
func TestScanAll_SliceOfPointer(t *testing.T) {
	type Row struct {
		ID int `db:"id"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2),
	)
	var got []*Row
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Errorf("got %+v", got)
	}
}

// Scalar destinations skip the reflection path entirely.
func TestScanAll_ScalarSlice(t *testing.T) {
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"v"}).AddRow("a").AddRow("b").AddRow("c"),
	)
	var got []string
	if err := scanAll(&got, query(t, db)); err != nil {
		t.Fatalf("scanAll: %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("got %+v", got)
	}
}

// scanOne returns sql.ErrNoRows on empty result sets.
func TestScanOne_NoRowsErr(t *testing.T) {
	type Row struct {
		ID int `db:"id"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	var got Row
	err := scanOne(&got, query(t, db))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("want sql.ErrNoRows, got %v", err)
	}
}

// scanOne errors when the query returned more than one row. Get is for
// single-row lookups; surfacing the plurality beats silently dropping data.
func TestScanOne_ErrorsOnExtraRows(t *testing.T) {
	type Row struct {
		ID int `db:"id"`
	}
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2),
	)
	var got Row
	err := scanOne(&got, query(t, db))
	if !errors.Is(err, ErrMultipleRows) {
		t.Errorf("want ErrMultipleRows, got %v", err)
	}
}

// scanOne handles scalar destinations.
func TestScanOne_Scalar(t *testing.T) {
	db, mock := openMock(t)
	mock.ExpectQuery("SELECT").WillReturnRows(
		sqlmock.NewRows([]string{"n"}).AddRow(42),
	)
	var n int
	if err := scanOne(&n, query(t, db)); err != nil {
		t.Fatalf("scanOne: %v", err)
	}
	if n != 42 {
		t.Errorf("want 42 got %d", n)
	}
}

// buildFieldMap exact-tag wins over a competing lowercased field name.
func TestBuildFieldMap_TagWinsOverFieldName(t *testing.T) {
	type Row struct {
		Foo string `db:"bar"`
	}
	m := buildFieldMap(reflect.TypeOf(Row{}))
	if _, ok := m["bar"]; !ok {
		t.Errorf("expected tag-named entry 'bar' in %+v", m)
	}
	if _, ok := m["foo"]; ok {
		t.Errorf("did not expect fallback entry 'foo' when tag set: %+v", m)
	}
}
