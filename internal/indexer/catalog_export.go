package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CatalogQueryer is implemented by *sql.DB and allows catalog extraction to
// be tested or embedded with any Oracle database/sql driver.
type CatalogQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type catalogRows struct {
	objects map[string]*CatalogObject
	ordered []*CatalogObject
}

func (c *catalogRows) add(schema, object, kind string) (*CatalogObject, error) {
	key := schema + "\x00" + object
	if found := c.objects[key]; found != nil {
		if found.Kind != kind {
			return nil, fmt.Errorf("Oracle devolvió tipos de objeto incompatibles para %s.%s: %s y %s", schema, object, found.Kind, kind)
		}
		return found, nil
	}
	entry := &CatalogObject{Schema: schema, Name: object, Kind: kind}
	c.objects[key] = entry
	c.ordered = append(c.ordered, entry)
	return entry, nil
}

type catalogRoutine struct {
	schema, object, member string
	subprogram             int64
	parent                 *CatalogObject
	entry                  *CatalogMember
	standalone             bool
}

// ExportCatalog reads Oracle's ALL_* dictionary views and returns the offline
// JSON snapshot accepted by Index. Passing schemas restricts repository-owned
// objects while PUBLIC synonyms remain available. An empty schema list exports
// every object visible to the connected user.
func ExportCatalog(ctx context.Context, db CatalogQueryer, schemas ...string) (CatalogSnapshot, error) {
	var snapshot CatalogSnapshot
	if db == nil {
		return snapshot, fmt.Errorf("exportar catálogo: conexión Oracle vacía")
	}
	schemas, err := normalizeExportSchemas(schemas)
	if err != nil {
		return snapshot, err
	}
	args := make([]any, len(schemas))
	for i, schema := range schemas {
		args[i] = schema
	}
	owners := ownerPredicate("o.OWNER", len(schemas), 1)
	catalog := &catalogRows{objects: map[string]*CatalogObject{}}

	objectQuery := `SELECT o.OWNER, o.OBJECT_NAME, o.OBJECT_TYPE
FROM ALL_OBJECTS o
WHERE o.OBJECT_TYPE IN ('TABLE','VIEW','PACKAGE','PROCEDURE','FUNCTION','SEQUENCE','SYNONYM','TYPE')` + owners + `
ORDER BY o.OWNER, o.OBJECT_NAME, o.OBJECT_TYPE`
	rows, err := db.QueryContext(ctx, objectQuery, args...)
	if err != nil {
		return snapshot, fmt.Errorf("consultar ALL_OBJECTS: %w", err)
	}
	for rows.Next() {
		var owner, objectName, objectType string
		if err := rows.Scan(&owner, &objectName, &objectType); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("leer ALL_OBJECTS: %w", err)
		}
		kind, ok := exportedObjectKind(objectType)
		if !ok {
			continue
		}
		if _, err := catalog.add(owner, objectName, kind); err != nil {
			rows.Close()
			return snapshot, err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("recorrer ALL_OBJECTS: %w", err)
	}
	if err := rows.Close(); err != nil {
		return snapshot, fmt.Errorf("cerrar ALL_OBJECTS: %w", err)
	}

	if err := exportColumns(ctx, db, catalog, "ALL_TAB_COLUMNS", "TABLE_NAME", "COLUMN_NAME", "DATA_TYPE", "COLUMN_ID", ownersFor("OWNER", len(schemas)), args); err != nil {
		return snapshot, err
	}
	if err := exportColumns(ctx, db, catalog, "ALL_TYPE_ATTRS", "TYPE_NAME", "ATTR_NAME", "ATTR_TYPE_NAME", "ATTR_NO", ownersFor("OWNER", len(schemas)), args); err != nil {
		return snapshot, err
	}
	if err := exportSynonyms(ctx, db, catalog, schemas, args); err != nil {
		return snapshot, err
	}
	if err := exportRoutines(ctx, db, catalog, schemas, args); err != nil {
		return snapshot, err
	}

	sort.Slice(catalog.ordered, func(i, j int) bool {
		a, b := catalog.ordered[i], catalog.ordered[j]
		if a.Schema != b.Schema {
			return a.Schema < b.Schema
		}
		return a.Name < b.Name
	})
	for _, object := range catalog.ordered {
		snapshot.Objects = append(snapshot.Objects, *object)
	}
	return snapshot, nil
}

func normalizeExportSchemas(schemas []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		schema = strings.TrimSpace(schema)
		if !validCatalogName(schema) {
			return nil, fmt.Errorf("esquema inválido en exportación de catálogo")
		}
		if seen[schema] {
			continue
		}
		seen[schema] = true
		result = append(result, schema)
	}
	sort.Strings(result)
	return result, nil
}

func ownerPredicate(column string, count, firstBind int) string {
	if count == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" AND ")
	b.WriteString(column)
	b.WriteString(" IN (")
	for i := 0; i < count; i++ {
		if i != 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, ":%d", firstBind+i)
	}
	b.WriteByte(')')
	return b.String()
}

func ownersFor(owner string, count int) string { return ownerPredicate(owner, count, 1) }

func exportedObjectKind(objectType string) (string, bool) {
	switch strings.ToUpper(objectType) {
	case "TABLE":
		return "table", true
	case "VIEW":
		return "view", true
	case "PACKAGE":
		return "package", true
	case "PROCEDURE":
		return "procedure", true
	case "FUNCTION":
		return "function", true
	case "SEQUENCE":
		return "sequence", true
	case "SYNONYM":
		return "synonym", true
	case "TYPE":
		return "type", true
	default:
		return "", false
	}
}

func exportColumns(ctx context.Context, db CatalogQueryer, catalog *catalogRows, view, tableColumn, nameColumn, typeColumn, orderColumn, ownerFilter string, args []any) error {
	query := fmt.Sprintf("SELECT OWNER, %s, %s, %s, %s FROM %s WHERE 1=1%s ORDER BY OWNER, %s, %s", tableColumn, nameColumn, typeColumn, orderColumn, view, ownerFilter, tableColumn, orderColumn)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("consultar %s: %w", view, err)
	}
	for rows.Next() {
		var owner, object, column, dataType string
		var ordinal int64
		if err := rows.Scan(&owner, &object, &column, &dataType, &ordinal); err != nil {
			rows.Close()
			return fmt.Errorf("leer %s: %w", view, err)
		}
		entry := catalog.objects[owner+"\x00"+object]
		if entry == nil || (entry.Kind != "table" && entry.Kind != "view" && entry.Kind != "type") {
			continue
		}
		entry.Columns = append(entry.Columns, CatalogColumn{Name: column, Type: dataType})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("recorrer %s: %w", view, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("cerrar %s: %w", view, err)
	}
	return nil
}

func exportSynonyms(ctx context.Context, db CatalogQueryer, catalog *catalogRows, schemas []string, args []any) error {
	ownerFilter := ""
	if len(schemas) > 0 {
		ownerFilter = " AND (s.OWNER='PUBLIC' OR s.OWNER IN ("
		for i := range schemas {
			if i != 0 {
				ownerFilter += ","
			}
			ownerFilter += fmt.Sprintf(":%d", i+1)
		}
		ownerFilter += "))"
	}
	rows, err := db.QueryContext(ctx, `SELECT s.OWNER, s.SYNONYM_NAME, s.TABLE_OWNER, s.TABLE_NAME, s.DB_LINK
FROM ALL_SYNONYMS s
WHERE 1=1`+ownerFilter+`
ORDER BY s.OWNER, s.SYNONYM_NAME`, args...)
	if err != nil {
		return fmt.Errorf("consultar ALL_SYNONYMS: %w", err)
	}
	for rows.Next() {
		var owner, synonymName, targetOwner, targetName string
		var dbLink sql.NullString
		if err := rows.Scan(&owner, &synonymName, &targetOwner, &targetName, &dbLink); err != nil {
			rows.Close()
			return fmt.Errorf("leer ALL_SYNONYMS: %w", err)
		}
		entry, err := catalog.add(owner, synonymName, "synonym")
		if err != nil {
			rows.Close()
			return err
		}
		entry.Target = &CatalogTarget{Schema: targetOwner, Name: targetName}
		if dbLink.Valid {
			entry.Target.DBLink = dbLink.String
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("recorrer ALL_SYNONYMS: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("cerrar ALL_SYNONYMS: %w", err)
	}
	return nil
}

func exportRoutines(ctx context.Context, db CatalogQueryer, catalog *catalogRows, schemas []string, args []any) error {
	filter := ownerPredicate("p.OWNER", len(schemas), 1)
	query := `SELECT p.OWNER, p.OBJECT_NAME, p.PROCEDURE_NAME, p.OBJECT_TYPE, p.SUBPROGRAM_ID,
       a.ARGUMENT_NAME, a.POSITION, a.SEQUENCE,
       COALESCE(a.TYPE_NAME, a.DATA_TYPE), a.IN_OUT, a.DEFAULTED
FROM ALL_PROCEDURES p
LEFT JOIN ALL_ARGUMENTS a
  ON a.OWNER=p.OWNER AND a.OBJECT_NAME=p.OBJECT_NAME
	   AND ((p.OBJECT_TYPE='PACKAGE' AND a.PACKAGE_NAME=p.OBJECT_NAME)
	     OR (p.OBJECT_TYPE='TYPE' AND (a.PACKAGE_NAME=p.OBJECT_NAME OR a.PACKAGE_NAME IS NULL))
   OR (p.OBJECT_TYPE IN ('PROCEDURE','FUNCTION') AND a.PACKAGE_NAME IS NULL))
 AND a.SUBPROGRAM_ID=p.SUBPROGRAM_ID
WHERE ((p.OBJECT_TYPE IN ('PACKAGE','TYPE') AND p.PROCEDURE_NAME IS NOT NULL)
    OR (p.OBJECT_TYPE IN ('PROCEDURE','FUNCTION') AND p.PROCEDURE_NAME IS NULL))` + filter + `
ORDER BY p.OWNER, p.OBJECT_NAME, p.PROCEDURE_NAME, p.SUBPROGRAM_ID, a.SEQUENCE`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("consultar ALL_PROCEDURES/ALL_ARGUMENTS: %w", err)
	}
	routines := map[string]*catalogRoutine{}
	var ordered []*catalogRoutine
	for rows.Next() {
		var owner, object, objectType string
		var member, argument, dataType, mode, defaulted sql.NullString
		var subprogram, position, sequence sql.NullInt64
		if err := rows.Scan(&owner, &object, &member, &objectType, &subprogram, &argument, &position, &sequence, &dataType, &mode, &defaulted); err != nil {
			rows.Close()
			return fmt.Errorf("leer ALL_PROCEDURES/ALL_ARGUMENTS: %w", err)
		}
		parent := catalog.objects[owner+"\x00"+object]
		if parent == nil || !subprogram.Valid {
			continue
		}
		standalone := objectType == "PROCEDURE" || objectType == "FUNCTION"
		memberName := ""
		if !standalone {
			if !member.Valid || member.String == "" || parent.Kind != "package" && parent.Kind != "type" {
				continue
			}
			memberName = member.String
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%d", owner, object, memberName, subprogram.Int64)
		routine := routines[key]
		if routine == nil {
			routine = &catalogRoutine{schema: owner, object: object, member: memberName, subprogram: subprogram.Int64, parent: parent, standalone: standalone}
			if standalone {
				routine.entry = &CatalogMember{Name: object}
			} else {
				routine.entry = &CatalogMember{Name: memberName}
			}
			routines[key] = routine
			ordered = append(ordered, routine)
		}
		if argument.Valid && argument.String != "" && position.Int64 > 0 {
			if objectType == "TYPE" && strings.EqualFold(argument.String, "SELF") {
				continue
			}
			typ := "UNKNOWN"
			if dataType.Valid && strings.TrimSpace(dataType.String) != "" {
				typ = strings.TrimSpace(dataType.String)
			}
			parameter := CatalogParameter{Name: argument.String, Type: typ}
			if mode.Valid {
				parameter.Mode = strings.ReplaceAll(strings.ToUpper(mode.String), "/", " ")
			}
			parameter.Default = defaulted.Valid && strings.EqualFold(defaulted.String, "Y")
			routine.entry.Parameters = append(routine.entry.Parameters, parameter)
		} else if position.Valid && position.Int64 == 0 {
			routine.entry.Kind = "function"
			if dataType.Valid {
				if standalone {
					routine.parent.ReturnType = strings.TrimSpace(dataType.String)
				} else {
					routine.entry.ReturnType = strings.TrimSpace(dataType.String)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("recorrer ALL_PROCEDURES/ALL_ARGUMENTS: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("cerrar ALL_PROCEDURES/ALL_ARGUMENTS: %w", err)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.schema != b.schema {
			return a.schema < b.schema
		}
		if a.object != b.object {
			return a.object < b.object
		}
		if a.member != b.member {
			return a.member < b.member
		}
		return a.subprogram < b.subprogram
	})
	standaloneSeen := map[string]bool{}
	for _, routine := range ordered {
		if routine.entry.Kind == "" {
			routine.entry.Kind = "procedure"
		}
		if routine.standalone {
			key := routine.schema + "\x00" + routine.object
			if standaloneSeen[key] {
				return fmt.Errorf("Oracle devolvió varias firmas para el subprograma standalone %s.%s", routine.schema, routine.object)
			}
			standaloneSeen[key] = true
			routine.parent.Parameters = routine.entry.Parameters
		} else {
			routine.parent.Members = append(routine.parent.Members, *routine.entry)
		}
	}
	return nil
}

// WriteCatalogJSON emits a stable snapshot suitable for --catalog.
func WriteCatalogJSON(w io.Writer, snapshot CatalogSnapshot) error {
	if w == nil {
		return fmt.Errorf("escritor de catálogo vacío")
	}
	snapshot.Objects = append([]CatalogObject(nil), snapshot.Objects...)
	sort.Slice(snapshot.Objects, func(i, j int) bool {
		if snapshot.Objects[i].Schema != snapshot.Objects[j].Schema {
			return snapshot.Objects[i].Schema < snapshot.Objects[j].Schema
		}
		return snapshot.Objects[i].Name < snapshot.Objects[j].Name
	})
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}
