package sqlg

import "strings"

// Pretty rendering: replicates sqlglot's pretty generator (indent=4, pad=4). Kept
// separate from compact rendering for byte-diffing against golden_roundtrip.json.

// prettyPad is the number of spaces per indentation level.
const prettyPad = 4

// Pretty renders the entire expression in pretty mode (root indent is 0).
func (d *Dialect) Pretty(e Expression) string { return d.pretty(e, 0) }

func pPad(n int) string { return strings.Repeat(" ", n) }

func (d *Dialect) pretty(e Expression, indent int) string {
	if e == nil {
		return ""
	}
	switch n := e.(type) {
	case *Select:
		return d.prettySelect(n, indent)
	case *Subquery:
		return d.prettySubquery(n, indent)
	case *Paren:
		return "(\n" + pPad(indent+prettyPad) + d.pretty(n.This, indent+prettyPad) + "\n" + pPad(indent) + ")"
	case *Union:
		s := d.pretty(n.Left, indent)
		op := n.Op
		if op == "" {
			op = "UNION"
		}
		if !n.Distinct {
			op += " ALL"
		}
		s = s + "\n" + pPad(indent) + op + "\n" + pPad(indent) + d.pretty(n.Right, indent)
		if n.With != nil {
			s = d.prettyWithPrefix(n.With, indent) + s
		}
		return s
	case *unionTail:
		s := d.pretty(n.Union, indent)
		if n.Order != nil {
			s += "\n" + pPad(indent) + "ORDER BY\n" + pPad(indent+prettyPad) + d.prettyOrderedList(n.Order, indent+prettyPad)
		}
		if n.Limit != nil {
			s += "\n" + pPad(indent) + n.Limit.SQL(d)
		}
		if n.Offset != nil {
			s += "\n" + pPad(indent) + n.Offset.SQL(d)
		}
		return s
	case *In:
		s := d.pretty(n.This, indent) + " IN "
		if n.Query != nil {
			return s + d.pretty(n.Query, indent)
		}
		parts := make([]string, 0, len(n.List))
		for _, x := range n.List {
			parts = append(parts, d.pretty(x, indent))
		}
		return s + "(" + strings.Join(parts, ", ") + ")"
	case *Func:
		return d.prettyFunc(n, indent)
	case *WhereClause:
		return "WHERE\n" + pPad(indent+prettyPad) + d.pretty(n.This, indent+prettyPad)
	case *Alias:
		return d.pretty(n.This, indent) + " AS " + n.Alias.SQL(d)
	case *Unary:
		if n.Op == "NOT" {
			return "NOT " + d.pretty(n.This, indent)
		}
		return n.Op + d.pretty(n.This, indent)
	case *Binary:
		return d.pretty(n.Left, indent) + " " + n.Op + " " + d.pretty(n.Right, indent)
	case *Is:
		s := d.pretty(n.This, indent) + " IS "
		if n.Not {
			s += "NOT "
		}
		if n.Expr == nil {
			return s + "NULL"
		}
		return s + d.pretty(n.Expr, indent)
	case *Exists:
		return "EXISTS" + d.pretty(n.This, indent)
	case *Between:
		return d.pretty(n.This, indent) + " BETWEEN " + d.pretty(n.Low, indent) + " AND " + d.pretty(n.High, indent)
	case *Case:
		var sb strings.Builder
		sb.WriteString("CASE")
		if n.Operand != nil {
			sb.WriteString(" " + d.pretty(n.Operand, indent))
		}
		for _, w := range n.Ifs {
			sb.WriteString(" WHEN " + d.pretty(w.Cond, indent) + " THEN " + d.pretty(w.Then, indent))
		}
		if n.Default != nil {
			sb.WriteString(" ELSE " + d.pretty(n.Default, indent))
		}
		sb.WriteString(" END")
		return sb.String()
	case *Cast:
		name := "CAST"
		if n.Try {
			name = "TRY_CAST"
		}
		to := ""
		if n.To != nil {
			to = d.renderCastTypeName(n.To.Name)
		}
		return name + "(" + d.pretty(n.This, indent) + " AS " + to + ")"
	case *Bracket:
		return d.pretty(n.This, indent) + "[" + d.pretty(n.Index, indent) + "]"
	case *Insert:
		return d.prettyInsert(n, indent)
	case *Update:
		return d.prettyUpdate(n, indent)
	case *Delete:
		return d.prettyDelete(n, indent)
	case *Create:
		return d.prettyCreate(n, indent)
	case *Schema:
		return d.prettySchema(n, indent)
	}
	return e.SQL(d)
}

func (d *Dialect) prettySelect(n *Select, indent int) string {
	inner := pPad(indent + prettyPad)
	var lines []string

	head := "SELECT"
	if n.Top != "" {
		head += " TOP " + n.Top
	}
	if n.Distinct {
		head += " DISTINCT"
	}
	exprs := make([]string, 0, len(n.Expressions))
	for _, e := range n.Expressions {
		exprs = append(exprs, d.pretty(e, indent+prettyPad))
	}
	lines = append(lines, head+"\n"+inner+strings.Join(exprs, ",\n"+inner))

	if n.From != nil {
		fromLine := "FROM " + d.pretty(n.From, indent)
		// prettyJoin carries its own leading separator: comma form stays on the FROM
		// line, others start a new line (mirrors sqlglot seg()).
		for _, j := range n.Joins {
			fromLine += d.prettyJoin(j, indent)
		}
		lines = append(lines, fromLine)
	}
	if n.Where != nil {
		lines = append(lines, "WHERE\n"+inner+d.pretty(n.Where, indent+prettyPad))
	}
	if n.Group != nil {
		gs := make([]string, 0, len(n.Group.Expressions))
		for _, g := range n.Group.Expressions {
			gs = append(gs, d.pretty(g, indent+prettyPad))
		}
		lines = append(lines, "GROUP BY\n"+inner+strings.Join(gs, ",\n"+inner))
	}
	if n.Having != nil {
		lines = append(lines, "HAVING\n"+inner+d.pretty(n.Having, indent+prettyPad))
	}
	if n.Order != nil {
		lines = append(lines, "ORDER BY\n"+inner+d.prettyOrderedList(n.Order, indent+prettyPad))
	}
	// With LIMIT_FETCH == "FETCH", the limit renders as exp.Fetch and OFFSET comes
	// first (Generator.offset_limit_modifiers).
	if d.FetchFirst && n.Limit != nil {
		if n.Offset != nil {
			lines = append(lines, n.Offset.SQL(d))
		}
		lines = append(lines, n.Limit.SQL(d))
	} else {
		if n.Limit != nil {
			lines = append(lines, n.Limit.SQL(d))
		}
		if n.Offset != nil {
			lines = append(lines, n.Offset.SQL(d))
		}
	}
	body := strings.Join(lines, "\n"+pPad(indent))
	if n.With != nil {
		return d.prettyWithPrefix(n.With, indent) + body
	}
	return body
}

func (d *Dialect) prettyOrderedList(o *Order, indent int) string {
	inner := pPad(indent)
	parts := make([]string, 0, len(o.Expressions))
	for _, x := range o.Expressions {
		s := d.pretty(x.This, indent)
		if x.Explicit {
			if x.Desc {
				s += " DESC"
			} else {
				s += " ASC"
			}
		}
		if x.NullsFirst != nil {
			if *x.NullsFirst {
				s += " NULLS FIRST"
			} else {
				s += " NULLS LAST"
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",\n"+inner)
}

// prettyJoin returns the join fragment WITH its leading separator. The comma form stays
// on the FROM line; all other forms start a new line aligned with FROM.
func (d *Dialect) prettyJoin(j *Join, indent int) string {
	if joinIsCommaForm(j) {
		return ", " + d.pretty(j.This, indent)
	}
	ops := []string{}
	if j.Side != "" {
		ops = append(ops, j.Side)
	}
	if j.Kind != "" {
		ops = append(ops, j.Kind)
	}
	ops = append(ops, "JOIN")
	s := "\n" + pPad(indent) + strings.Join(ops, " ") + " " + d.pretty(j.This, indent)
	if j.On != nil {
		s += "\n" + pPad(indent+prettyPad) + "ON " + d.pretty(j.On, indent+prettyPad)
	} else if len(j.Using) > 0 {
		parts := make([]string, 0, len(j.Using))
		for _, u := range j.Using {
			parts = append(parts, u.SQL(d))
		}
		s += "\n" + pPad(indent+prettyPad) + "USING (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

func (d *Dialect) prettyWithPrefix(n *With, indent int) string {
	inner := pPad(indent + prettyPad)
	parts := make([]string, 0, len(n.CTEs))
	for _, c := range n.CTEs {
		parts = append(parts, c.AliasName()+" AS (\n"+inner+d.pretty(c.This.This, indent+prettyPad)+"\n"+pPad(indent)+")")
	}
	s := "WITH "
	if n.Recursive {
		s += "RECURSIVE "
	}
	return s + strings.Join(parts, ", ") + "\n" + pPad(indent)
}

func (d *Dialect) prettySubquery(n *Subquery, indent int) string {
	s := "(\n" + pPad(indent+prettyPad) + d.pretty(n.This, indent+prettyPad) + "\n" + pPad(indent) + ")"
	if n.Alias != nil {
		// AS is controlled by TableAliasAS (Oracle omits it).
		if d.TableAliasAS {
			return s + " AS " + d.renderTableAlias(n.Alias)
		}
		return s + " " + d.renderTableAlias(n.Alias)
	}
	return s
}

// prettyFunc only switches arguments to multi-line; the rest shares the compact-path
// implementation (pretty is a flag on the same generator in sqlglot).
func (d *Dialect) prettyFunc(n *Func, indent int) string {
	return d.renderFuncWith(n, func(e Expression) string { return d.pretty(e, indent) })
}

func (d *Dialect) prettyInsert(n *Insert, indent int) string {
	inner := pPad(indent + prettyPad)
	s := "INSERT INTO " + d.pretty(n.Table, indent)
	if len(n.Columns) > 0 {
		parts := make([]string, 0, len(n.Columns))
		for _, c := range n.Columns {
			parts = append(parts, c.SQL(d))
		}
		s += " (\n" + inner + strings.Join(parts, ",\n"+inner) + "\n" + pPad(indent) + ")"
	}
	if len(n.Values) > 0 {
		rows := make([]string, 0, len(n.Values))
		for _, row := range n.Values {
			vals := make([]string, 0, len(row))
			for _, v := range row {
				vals = append(vals, d.pretty(v, indent+prettyPad))
			}
			rows = append(rows, "("+strings.Join(vals, ", ")+")")
		}
		return s + "\nVALUES\n" + inner + strings.Join(rows, ",\n"+inner)
	}
	if n.Query != nil {
		return s + "\n" + d.pretty(n.Query, indent)
	}
	return s
}

func (d *Dialect) prettyUpdate(n *Update, indent int) string {
	parts := make([]string, 0, len(n.Set))
	for _, kv := range n.Set {
		parts = append(parts, kv[0].SQL(d)+" = "+kv[1].SQL(d))
	}
	s := "UPDATE " + d.pretty(n.Table, indent) + " SET " + strings.Join(parts, ", ")
	if n.Where != nil {
		s += "\n" + pPad(indent) + "WHERE\n" + pPad(indent+prettyPad) + d.pretty(n.Where, indent+prettyPad)
	}
	return s
}

// prettyDelete replicates Generator.delete_sql; pretty WHERE has the same shape as UPDATE.
func (d *Dialect) prettyDelete(n *Delete, indent int) string {
	s := "DELETE FROM " + d.pretty(n.Table, indent)
	if n.Where != nil {
		s += "\n" + pPad(indent) + "WHERE\n" + pPad(indent+prettyPad) + d.pretty(n.Where, indent+prettyPad)
	}
	if n.Limit != nil {
		// delete_sql uses the base limit_sql (no pretty branch).
		s += limitSQLPlain(d, n.Limit)
	}
	return s
}

// prettyCreate replicates Generator.create_sql; pretty differs from compact only in the
// Schema (column definitions expand line by line).
func (d *Dialect) prettyCreate(n *Create, indent int) string {
	this := ""
	if n.This != nil {
		this = d.pretty(n.This, indent)
	}
	return "CREATE " + n.Kind + " " + this
}

// prettySchema replicates Generator.schema_columns_sql. expressions() uses the default
// level=0, so columns are always indented by pad(4) and the right paren sits at column 0
// — do not propagate the outer indent (matches sqlglot).
func (d *Dialect) prettySchema(n *Schema, indent int) string {
	this := ""
	if n.This != nil {
		this = d.pretty(n.This, indent)
	}
	if len(n.Expressions) == 0 {
		return this
	}
	inner := pPad(prettyPad)
	parts := make([]string, 0, len(n.Expressions))
	for _, e := range n.Expressions {
		parts = append(parts, d.pretty(e, prettyPad))
	}
	return this + " (\n" + inner + strings.Join(parts, ",\n"+inner) + "\n)"
}
