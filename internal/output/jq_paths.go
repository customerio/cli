package output

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/itchyny/gojq"
)

// MissingJQPaths lists root paths expr iterates or reads optionally that data lacks, since jq then prints nothing instead of null.
func MissingJQPaths(data json.RawMessage, expr string) []string {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil
	}

	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}

	found := map[string]bool{}
	collectMissing(query, root, found)
	if len(found) == 0 {
		return nil
	}

	return slices.Sorted(maps.Keys(found))
}

// collectMissing walks only the parts of q whose input is the root value.
func collectMissing(q *gojq.Query, root any, found map[string]bool) {
	if q == nil {
		return
	}

	switch q.Op {
	case 0:
		collectTerm(q.Term, root, found)
	case gojq.OpPipe:
		collectMissing(q.Left, root, found)
	case gojq.OpAlt, gojq.OpAssign, gojq.OpUpdateAdd, gojq.OpUpdateSub, gojq.OpUpdateMul, gojq.OpUpdateDiv, gojq.OpUpdateMod, gojq.OpUpdateAlt:
		// The left side of `//` may be missing by design, and an assignment's left side is a path it creates.
		collectMissing(q.Right, root, found)
	case gojq.OpModify:
		// Both sides of `|=` work on the value at the path, not the root.
	default:
		collectMissing(q.Left, root, found)
		collectMissing(q.Right, root, found)
	}
}

func collectTerm(t *gojq.Term, root any, found map[string]bool) {
	if t == nil {
		return
	}

	switch t.Type {
	case gojq.TermTypeIndex:
		if p := missingIndexPath(t, root); p != "" {
			found[p] = true
		}
	case gojq.TermTypeObject:
		for _, kv := range t.Object.KeyVals {
			collectMissing(kv.Val, root, found)
		}
	case gojq.TermTypeArray:
		collectMissing(t.Array.Query, root, found)
	case gojq.TermTypeQuery:
		if len(t.SuffixList) == 0 {
			collectMissing(t.Query, root, found)
		}
	}
}

// missingIndexPath follows a `.a.b["c"]` chain that ends in `[]` or `?` and
// returns the path up to the first key its object lacks.
func missingIndexPath(t *gojq.Term, root any) string {
	k, ok := indexKey(t.Index)
	if !ok {
		return ""
	}

	keys := []string{k}
	silent := false
	for _, s := range t.SuffixList {
		silent = silent || s.Iter || s.Optional
		k, ok := indexKey(s.Index)
		if !ok {
			break
		}

		keys = append(keys, k)
	}
	if !silent {
		return ""
	}

	cur := root
	var path strings.Builder
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}

		path.WriteString("." + k)
		next, has := obj[k]
		if !has {
			return path.String()
		}
		cur = next
	}
	return ""
}

func indexKey(i *gojq.Index) (string, bool) {
	if i == nil || i.IsSlice {
		return "", false
	}

	if i.Name != "" {
		return i.Name, true
	}

	str := i.Str
	if s := i.Start; s != nil && s.Op == 0 && s.Term != nil && s.Term.Type == gojq.TermTypeString && len(s.Term.SuffixList) == 0 {
		str = s.Term.Str
	}
	if str != nil && str.Queries == nil {
		return str.Str, true
	}
	return "", false
}
