package remote

// ForbidSelfApprovalForTest requires a second operator for commandType until
// the test ends.
func ForbidSelfApprovalForTest(t interface{ Cleanup(func()) }, commandType string) {
	policiesMu.Lock()
	original := policies[commandType]
	changed := original
	changed.ForbidSelfApproval = true
	policies[commandType] = changed
	policiesMu.Unlock()
	t.Cleanup(func() {
		policiesMu.Lock()
		policies[commandType] = original
		policiesMu.Unlock()
	})
}
