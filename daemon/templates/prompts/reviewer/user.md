Task request:
{{.Request}}

Approved plan:
{{.Plan}}

Checks:
{{.Checks}}

Git-derived changed files:
{{.ChangedFiles}}

Git diff:
{{range .Diff}}
{{.Patch}}
{{end}}
