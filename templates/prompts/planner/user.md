Feature request:
{{.Request}}

Inspect {{.Repository}} and produce concrete steps with expected_files and acceptance_criteria.

{{if .CurrentPlan}}Revise the current plan below using the user's feedback. Preserve correct steps and resolve the listed questions.
Current plan:
{{.CurrentPlan}}
Unresolved questions:
{{range .Questions}}- {{.}}
{{end}}User feedback:
{{.Feedback}}
{{end}}
Return only the JSON envelope. Put unresolved decisions only in questions; put implementation work only in steps.
