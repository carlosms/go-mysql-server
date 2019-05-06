package analyzer

import (
	"fmt"

	"gopkg.in/src-d/go-mysql-server.v0/sql"
	"gopkg.in/src-d/go-mysql-server.v0/sql/expression"
	"gopkg.in/src-d/go-mysql-server.v0/sql/expression/function"
	"gopkg.in/src-d/go-mysql-server.v0/sql/plan"
)

type tableCol struct {
	table string
	col   string
}

// convertDates wraps all expressions of date and datetime type with converts
// to ensure the date range is validated.
func convertDates(ctx *sql.Context, a *Analyzer, n sql.Node) (sql.Node, error) {
	if !n.Resolved() {
		return n, nil
	}

	// Replacements contains a mapping from columns to the alias they will be
	// replaced by.
	var replacements = make(map[tableCol]string)

	return n.TransformUp(func(n sql.Node) (sql.Node, error) {
		exp, ok := n.(sql.Expressioner)
		if !ok {
			return n, nil
		}

		// nodeReplacements are all the replacements found in the current node.
		// These replacements are not applied to the current node, only to
		// parent nodes.
		var nodeReplacements = make(map[tableCol]string)

		var expressions = make(map[string]bool)
		switch exp := exp.(type) {
		case *plan.Project:
			for _, e := range exp.Projections {
				expressions[e.String()] = true
			}
		case *plan.GroupBy:
			for _, e := range exp.Aggregate {
				expressions[e.String()] = true
			}
		}

		result, err := exp.TransformExpressions(func(e sql.Expression) (sql.Expression, error) {
			var result sql.Expression

			// No need to wrap expressions that already validate times, such as
			// convert, date_add, etc and those expressions whose Type method
			// cannot be called because they are placeholders.
			switch e.(type) {
			case *expression.Convert,
				*expression.Arithmetic,
				*function.DateAdd,
				*function.DateSub,
				*expression.Star,
				*expression.DefaultColumn,
				*expression.Alias:
				return e, nil
			default:
				// If it's a replacement, just replace it with the correct GetField
				// because we know that it's already converted to a correct date
				// and there is no point to do so again.
				if gf, ok := e.(*expression.GetField); ok {
					if name, ok := replacements[tableCol{gf.Table(), gf.Name()}]; ok {
						return expression.NewGetField(gf.Index(), gf.Type(), name, gf.IsNullable()), nil
					}
				}

				switch e.Type() {
				case sql.Date:
					result = expression.NewConvert(e, expression.ConvertToDate)
				case sql.Timestamp:
					result = expression.NewConvert(e, expression.ConvertToDatetime)
				default:
					result = e
				}
			}

			// Only do this if it's a root expression in a project or group by.
			switch exp.(type) {
			case *plan.Project, *plan.GroupBy:
				// If it was originally a GetField, and it's not anymore it's
				// because we wrapped it in a convert. We need to make it an alias
				// and propagate the changes up the chain.
				if gf, ok := e.(*expression.GetField); ok && expressions[e.String()] {
					if _, ok := result.(*expression.GetField); !ok {
						name := fmt.Sprintf("%s__%s", gf.Table(), gf.Name())
						result = expression.NewAlias(result, name)
						nodeReplacements[tableCol{gf.Table(), gf.Name()}] = name
					}
				}
			}

			return result, nil
		})

		// We're done with this node, so copy all the replacements found in
		// this node to the global replacements in order to make the necesssary
		// changes in parent nodes.
		for tc, n := range nodeReplacements {
			replacements[tc] = n
		}

		return result, err
	})
}
