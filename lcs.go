package jsondiff

// lcs computes the longest common subsequence of two slices and
// returns the index pairs of the LCS, that is, the indices into
// the source and target slices where the LCS items are located.
//
// It is a thin wrapper over matcherLCS using a stateless
// interner, so internal callers and the production array diff
// share the exact same LCS/equality logic.
func lcs(src, tgt []interface{}) [][2]int {
	d := &Differ{}
	in := newInterner(d)
	_ = in.internArray(src, tgt)
	return matcherLCS(in, src, tgt, internerSavepoint{}, nil)
}

// matcherLCS builds the LCS of src and tgt using equality
// verdicts produced by the interner. Every element pair is
// digested once and compared at most once: the DP fill records
// verdicts through the interner memo and backtracking reuses
// them.
//
// savepoint allows the caller to rewind tokens registered for
// this array when an injected stage failure aborts the match;
// hooks, when non-nil, are invoked at deterministic stages.
func matcherLCS(in *valueInterner, src, tgt []interface{}, sp internerSavepoint, hooks *matchHooks) [][2]int {
	invokeStage := func(s string) {
		if hooks == nil || hooks.stage == nil {
			return
		}
		if err := hooks.stage(s); err != nil {
			in.rewind(sp)
			panic(matchPanic{err: err})
		}
	}

	invokeStage(stageBeforeIntern)
	srcTok, _, tgtTok, _ := in.internArray(src, tgt)
	in.stats.ArraysMatched++

	invokeStage(stageBeforeLCS)

	n, m := len(src), len(tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if in.equalTokens(srcTok[i-1], tgtTok[j-1]) == matchVerdictEqual {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}

	invokeStage(stageAfterLCS)

	i, j := n, m
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case in.equalTokens(srcTok[i-1], tgtTok[j-1]) == matchVerdictEqual:
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}
