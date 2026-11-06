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

// PLScopeIdentifier is one compiler-reported identifier from ALL_IDENTIFIERS.
// It is useful as a reference corpus when checking the indexer's resolution.
type PLScopeIdentifier struct {
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	Signature      string `json:"signature,omitempty"`
	Type           string `json:"type"`
	ObjectName     string `json:"object_name"`
	ObjectType     string `json:"object_type"`
	Usage          string `json:"usage"`
	UsageID        int64  `json:"usage_id"`
	Line           int64  `json:"line"`
	Column         int64  `json:"column"`
	UsageContextID int64  `json:"usage_context_id"`
}

// PLScopeStatement is a compiler-reported SQL statement usage from
// ALL_STATEMENTS. Text is Oracle's normalized statement text when available.
type PLScopeStatement struct {
	Owner            string `json:"owner"`
	ObjectName       string `json:"object_name"`
	ObjectType       string `json:"object_type"`
	Signature        string `json:"signature,omitempty"`
	Type             string `json:"type"`
	UsageID          int64  `json:"usage_id"`
	Line             int64  `json:"line"`
	Column           int64  `json:"column"`
	UsageContextID   int64  `json:"usage_context_id"`
	SQLID            string `json:"sql_id,omitempty"`
	HasHint          string `json:"has_hint,omitempty"`
	HasIntoBulk      string `json:"has_into_bulk,omitempty"`
	HasIntoReturning string `json:"has_into_returning,omitempty"`
	HasIntoRecord    string `json:"has_into_record,omitempty"`
	HasCurrentOf     string `json:"has_current_of,omitempty"`
	HasForUpdate     string `json:"has_for_update,omitempty"`
	HasInBinds       string `json:"has_in_binds,omitempty"`
	Text             string `json:"text,omitempty"`
	FullText         string `json:"full_text,omitempty"`
}

// PLScopeSnapshot combines the compiler's identifier and SQL statement
// reports for one owner/object selection.
type PLScopeSnapshot struct {
	Identifiers []PLScopeIdentifier `json:"identifiers"`
	Statements  []PLScopeStatement  `json:"statements"`
}

// ExportPLScope extracts compiler-resolved identifiers for one schema. If
// objectName is empty, all PL/Scope rows visible for that owner are returned.
func ExportPLScope(ctx context.Context, db CatalogQueryer, owner, objectName string) ([]PLScopeIdentifier, error) {
	if db == nil {
		return nil, fmt.Errorf("extraer PL/Scope: conexión Oracle vacía")
	}
	owner = strings.TrimSpace(owner)
	objectName = strings.TrimSpace(objectName)
	if owner == "" {
		return nil, fmt.Errorf("extraer PL/Scope: --schema es obligatorio")
	}
	query := `SELECT OWNER, NAME, SIGNATURE, TYPE, OBJECT_NAME, OBJECT_TYPE,
       USAGE, USAGE_ID, LINE, COL, USAGE_CONTEXT_ID
FROM ALL_IDENTIFIERS
	WHERE OWNER=:1`
	args := []any{owner}
	if objectName != "" {
		query += " AND OBJECT_NAME=:2"
		args = append(args, objectName)
	}
	query += " ORDER BY OBJECT_NAME, OBJECT_TYPE, LINE, COL, USAGE_ID"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("consultar ALL_IDENTIFIERS: %w", err)
	}
	defer rows.Close()
	identifiers := make([]PLScopeIdentifier, 0)
	for rows.Next() {
		var row PLScopeIdentifier
		var signature sql.NullString
		if err := rows.Scan(&row.Owner, &row.Name, &signature, &row.Type, &row.ObjectName,
			&row.ObjectType, &row.Usage, &row.UsageID, &row.Line, &row.Column, &row.UsageContextID); err != nil {
			return nil, fmt.Errorf("leer ALL_IDENTIFIERS: %w", err)
		}
		if signature.Valid {
			row.Signature = signature.String
		}
		identifiers = append(identifiers, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recorrer ALL_IDENTIFIERS: %w", err)
	}
	return identifiers, nil
}

// WritePLScopeJSON serializes a PL/Scope extraction deterministically.
func WritePLScopeJSON(w io.Writer, identifiers []PLScopeIdentifier) error {
	if w == nil {
		return fmt.Errorf("escritor PL/Scope vacío")
	}
	sort.Slice(identifiers, func(i, j int) bool {
		a, b := identifiers[i], identifiers[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.ObjectName != b.ObjectName {
			return a.ObjectName < b.ObjectName
		}
		if a.ObjectType != b.ObjectType {
			return a.ObjectType < b.ObjectType
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.UsageID < b.UsageID
	})
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(identifiers)
}

// ExportPLScopeStatements extracts PL/Scope statement metadata for one schema.
// Oracle exposes this view from 12.2; TEXT is null for statements without a
// normalized form.
func ExportPLScopeStatements(ctx context.Context, db CatalogQueryer, owner, objectName string) ([]PLScopeStatement, error) {
	if db == nil {
		return nil, fmt.Errorf("extraer PL/Scope: conexión Oracle vacía")
	}
	owner = strings.TrimSpace(owner)
	objectName = strings.TrimSpace(objectName)
	if owner == "" {
		return nil, fmt.Errorf("extraer PL/Scope: --schema es obligatorio")
	}
	query := `SELECT OWNER, OBJECT_NAME, OBJECT_TYPE, SIGNATURE, TYPE, USAGE_ID,
       LINE, COL, USAGE_CONTEXT_ID, SQL_ID, HAS_HINT, HAS_INTO_BULK,
       HAS_INTO_RETURNING, HAS_INTO_RECORD, HAS_CURRENT_OF, HAS_FOR_UPDATE,
		HAS_IN_BINDS, TEXT, FULL_TEXT
FROM ALL_STATEMENTS
WHERE OWNER=:1`
	args := []any{owner}
	if objectName != "" {
		query += " AND OBJECT_NAME=:2"
		args = append(args, objectName)
	}
	query += " ORDER BY OBJECT_NAME, OBJECT_TYPE, LINE, COL, USAGE_ID"
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("consultar ALL_STATEMENTS: %w", err)
	}
	defer rows.Close()
	statements := make([]PLScopeStatement, 0)
	for rows.Next() {
		var row PLScopeStatement
		var signature, sqlID, hint, intoBulk, intoReturning, intoRecord sql.NullString
		var currentOf, forUpdate, inBinds, textValue, fullText sql.NullString
		if err := rows.Scan(&row.Owner, &row.ObjectName, &row.ObjectType, &signature, &row.Type,
			&row.UsageID, &row.Line, &row.Column, &row.UsageContextID, &sqlID, &hint,
			&intoBulk, &intoReturning, &intoRecord, &currentOf, &forUpdate, &inBinds, &textValue, &fullText); err != nil {
			return nil, fmt.Errorf("leer ALL_STATEMENTS: %w", err)
		}
		row.Signature = nullableString(signature)
		row.SQLID = nullableString(sqlID)
		row.HasHint = nullableString(hint)
		row.HasIntoBulk = nullableString(intoBulk)
		row.HasIntoReturning = nullableString(intoReturning)
		row.HasIntoRecord = nullableString(intoRecord)
		row.HasCurrentOf = nullableString(currentOf)
		row.HasForUpdate = nullableString(forUpdate)
		row.HasInBinds = nullableString(inBinds)
		row.Text = nullableString(textValue)
		row.FullText = nullableString(fullText)
		statements = append(statements, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recorrer ALL_STATEMENTS: %w", err)
	}
	return statements, nil
}

// WritePLScopeStatementsJSON emits a deterministic ALL_STATEMENTS snapshot.
func WritePLScopeStatementsJSON(w io.Writer, statements []PLScopeStatement) error {
	if w == nil {
		return fmt.Errorf("escritor PL/Scope vacío")
	}
	sort.Slice(statements, func(i, j int) bool {
		a, b := statements[i], statements[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.ObjectName != b.ObjectName {
			return a.ObjectName < b.ObjectName
		}
		if a.ObjectType != b.ObjectType {
			return a.ObjectType < b.ObjectType
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.UsageID < b.UsageID
	})
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(statements)
}

// WritePLScopeSnapshotJSON emits identifier and statement reports together.
func WritePLScopeSnapshotJSON(w io.Writer, snapshot PLScopeSnapshot) error {
	if w == nil {
		return fmt.Errorf("escritor PL/Scope vacío")
	}
	sort.Slice(snapshot.Identifiers, func(i, j int) bool {
		a, b := snapshot.Identifiers[i], snapshot.Identifiers[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.ObjectName != b.ObjectName {
			return a.ObjectName < b.ObjectName
		}
		if a.ObjectType != b.ObjectType {
			return a.ObjectType < b.ObjectType
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.UsageID < b.UsageID
	})
	sort.Slice(snapshot.Statements, func(i, j int) bool {
		a, b := snapshot.Statements[i], snapshot.Statements[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.ObjectName != b.ObjectName {
			return a.ObjectName < b.ObjectName
		}
		if a.ObjectType != b.ObjectType {
			return a.ObjectType < b.ObjectType
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.UsageID < b.UsageID
	})
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}

func nullableString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
