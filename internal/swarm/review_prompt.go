package swarm

// Default review templates per agent type.
var defaultReviewTemplates = map[string]string{
	"cc": `Review the code in this project. Focus on:
1. Potential bugs or edge cases
2. Security vulnerabilities
3. Performance concerns
4. Code clarity and maintainability

Do NOT make any changes. Only provide analysis and recommendations.`,

	"cod": `Analyze the codebase and identify:
- Code quality issues
- Missing error handling
- Test coverage gaps
- Documentation needs

Output findings only, no modifications.`,

	"gmi": `Perform a code review focusing on:
- Architecture patterns
- API design
- Dependency concerns
- Scalability considerations

Analysis mode only - no code changes.`,
}

// ReviewOptions configures review prompt generation.
type ReviewOptions struct {
	FocusArea   string // e.g., "security", "performance"
	FilePattern string // e.g., "*.go", "internal/**"
}
