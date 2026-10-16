# E2E harness design

Applies when a file under `e2e/` changed this turn.

e2e/harness_test.go knows nothing about test success or failure. Helpers return plain
data and errors; every pass/fail decision belongs to the calling test. Helpers never call
t.Fatal/t.Error/t.Skip — *testing.T may appear in a helper only for resource cleanup
(t.Cleanup, t.TempDir).
