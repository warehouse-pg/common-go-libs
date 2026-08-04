package dbconn

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

/*
 * scanOne scans exactly one row from rows into dest. dest must be a non-nil
 * pointer. If dest points at a struct with mappable fields, columns are
 * matched to struct fields via "db" tags (falling back to lowercased field
 * name), and a result column with no destination field is an error. If dest
 * points at a scalar, a sql.Scanner, or a struct with no mappable fields
 * (e.g. time.Time), the single column value is scanned directly.
 *
 * Returns sql.ErrNoRows if the query produced no rows, and ErrMultipleRows
 * if it produced more than one. dbconn.Get is meant for queries the caller
 * knows return a single row (catalog lookups by PK, scalar functions,
 * SHOW <guc>, etc.); pluralizing those is almost always a bug, so we surface
 * it instead of silently taking the first row.
 */
func scanOne(dest interface{}, rows *sql.Rows) error {
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return errors.New("destination must be a non-nil pointer")
	}
	var err error
	if isMappableStruct(dv.Type()) {
		var paths [][]int
		if paths, err = columnPaths(rows, dv.Type().Elem()); err == nil {
			err = scanRowIntoStruct(rows, dv.Elem(), paths, make([]interface{}, len(paths)))
		}
	} else {
		err = rows.Scan(dest)
	}
	if err != nil {
		return err
	}
	if rows.Next() {
		return ErrMultipleRows
	}
	return rows.Err()
}

// ErrMultipleRows is returned by Get when a query produced more than one row.
var ErrMultipleRows = errors.New("dbconn.Get: query returned more than one row")

/*
 * scanAll scans every row into dest. dest must be a non-nil pointer to a
 * slice. The slice element type may be a struct (mapped via "db" tags) or a
 * scalar/sql.Scanner type for single-column queries.
 */
func scanAll(dest interface{}, rows *sql.Rows) error {
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return errors.New("destination must be a non-nil pointer to a slice")
	}
	sliceVal := dv.Elem()
	if sliceVal.Kind() != reflect.Slice {
		return errors.New("destination must point to a slice")
	}
	elemType := sliceVal.Type().Elem()

	isPtr := elemType.Kind() == reflect.Ptr
	baseType := elemType
	if isPtr {
		baseType = elemType.Elem()
	}

	if !isMappableStruct(reflect.PointerTo(baseType)) {
		for rows.Next() {
			newElemPtr := reflect.New(baseType)
			if err := rows.Scan(newElemPtr.Interface()); err != nil {
				return err
			}
			if isPtr {
				sliceVal.Set(reflect.Append(sliceVal, newElemPtr))
			} else {
				sliceVal.Set(reflect.Append(sliceVal, newElemPtr.Elem()))
			}
		}
		return rows.Err()
	}

	/*
	 * Struct rows: the column-to-field mapping is invariant across rows, so
	 * resolve it once up front. The per-row work is only refilling the field
	 * addresses into the reused scanArgs slice.
	 */
	paths, err := columnPaths(rows, baseType)
	if err != nil {
		return err
	}
	scanArgs := make([]interface{}, len(paths))
	for rows.Next() {
		newElemPtr := reflect.New(baseType)
		if err := scanRowIntoStruct(rows, newElemPtr.Elem(), paths, scanArgs); err != nil {
			return err
		}
		if isPtr {
			sliceVal.Set(reflect.Append(sliceVal, newElemPtr))
		} else {
			sliceVal.Set(reflect.Append(sliceVal, newElemPtr.Elem()))
		}
	}
	return rows.Err()
}

/*
 * isMappableStruct reports whether ptrType points at a struct that should be
 * filled field by field. Two kinds of struct are instead handed to
 * database/sql whole: those implementing sql.Scanner, and those with no
 * mappable fields at all - notably time.Time, whose fields are all
 * unexported - which the driver populates directly. Routing the latter
 * through field mapping would silently zero-fill them.
 */
func isMappableStruct(ptrType reflect.Type) bool {
	if implementsScanner(ptrType) {
		return false
	}
	t := ptrType.Elem()
	return t.Kind() == reflect.Struct && len(buildFieldMap(t)) > 0
}

/*
 * columnPaths resolves each result column to a struct field index path. A
 * column with no destination field is an error rather than being silently
 * discarded: a db-tag typo or a renamed column should fail the query, the
 * way sqlx's "missing destination name" did, not quietly produce zero
 * values.
 */
func columnPaths(rows *sql.Rows, t reflect.Type) ([][]int, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	fieldMap := buildFieldMap(t)
	paths := make([][]int, len(cols))
	for i, col := range cols {
		idx, ok := fieldMap[col]
		if !ok {
			idx, ok = fieldMap[strings.ToLower(col)]
		}
		if !ok {
			return nil, fmt.Errorf("missing destination field %q in %s", col, t)
		}
		paths[i] = idx
	}
	return paths, nil
}

func scanRowIntoStruct(rows *sql.Rows, structVal reflect.Value, paths [][]int, scanArgs []interface{}) error {
	for i, path := range paths {
		scanArgs[i] = fieldByIndexAlloc(structVal, path).Addr().Interface()
	}
	return rows.Scan(scanArgs...)
}

/*
 * fieldByIndexAlloc is reflect.Value.FieldByIndex, except that it allocates
 * nil embedded struct pointers along the path instead of panicking on them.
 */
func fieldByIndexAlloc(v reflect.Value, index []int) reflect.Value {
	for i, x := range index {
		if i > 0 && v.Kind() == reflect.Ptr {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v
}

/*
 * buildFieldMap maps column name -> field index path. The name comes from a
 * `db:"..."` tag when present, otherwise the lowercased field name; fields
 * tagged `db:"-"` are skipped.
 *
 * reflect.VisibleFields applies Go's own field promotion rules: embedded
 * structs (values or pointers) are flattened, and a field shadowed by a
 * shallower one is excluded, so an embedded field can never hijack a column
 * from the outer struct. When two visible fields still claim the same name
 * via tags, the shallower field wins.
 */
func buildFieldMap(t reflect.Type) map[string][]int {
	out := make(map[string][]int)
	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous {
			ft := f.Type
			if ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			// An embedded struct is a container whose fields are already
			// promoted into this list, not a column target itself - unless
			// it scans as a whole via sql.Scanner.
			if ft.Kind() == reflect.Struct && !implementsScanner(reflect.PointerTo(ft)) {
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("db")
		if tag == "-" {
			continue
		}
		name := tag
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		if old, exists := out[name]; !exists || len(f.Index) < len(old) {
			out[name] = f.Index
		}
	}
	return out
}

var scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()

func implementsScanner(t reflect.Type) bool {
	return t.Implements(scannerType)
}
