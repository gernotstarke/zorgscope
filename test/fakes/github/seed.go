package githubfake

import "time"

// Seed fills the server with a small, deterministic data set relative to now:
//   - arc42/arc42-template: #236 answered by gernotstarke, but its last activity is 51 d old — past
//     the 30 d stale_after default, so under the domain rules it evaluates to STALE, not merely aged;
//     #240 opened 2 h ago without comments (NEW, inside the 4 h grace); #233 30 d old with last
//     comment by the opener (UNANSWERED); PR #237 20 d old, no comments (UNANSWERED); latest run on
//     master succeeded.
//   - arc42/arc42.org-site: #12 answered, last activity 2 d old — well inside stale_after, so this one
//     stays merely aged; latest run on main FAILED (BUILD FAILED).
//   - one mention notification in an unmonitored repo.
func Seed(s *Server, now time.Time) {
	s.mu.Lock()
	s.seedNow = now
	s.mu.Unlock()
	d := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	s.AddRepo(Repo{Owner: "arc42", Name: "arc42-template", DefaultBranch: "master",
		Issues: []Issue{
			{Number: 236, Title: "Add/Replace images/arc42-logo.png with high quality image (vector?)", Author: "lwbt", CreatedAt: d(24 * 65), UpdatedAt: d(24 * 51),
				Comments: []Comment{{Author: "gernotstarke", At: d(24 * 51)}}},
			{Number: 240, Title: "Typo in section 8 of the EN template", Author: "newcomer", CreatedAt: d(2), UpdatedAt: d(2)},
			{Number: 233, Title: "Consider using GitHub Releases instead of committing build artifacts", Author: "lwbt", CreatedAt: d(24 * 30), UpdatedAt: d(24 * 29),
				Labels: []string{"enhancement"}, Comments: []Comment{{Author: "gernotstarke", At: d(24 * 29.5)}, {Author: "lwbt", At: d(24 * 29)}}},
			{Number: 237, Title: "Add example stakeholder table to EN Section 1", Author: "Sofeso", CreatedAt: d(24 * 20), UpdatedAt: d(24 * 19),
				IsPR: true, ReviewDecision: "REVIEW_REQUIRED", Labels: []string{"documentation"}},
		},
		Runs: []Run{{ID: 1001, Name: "build", Status: "completed", Conclusion: "success", Branch: "master",
			URL: "https://github.com/arc42/arc42-template/actions/runs/1001", CreatedAt: d(6), UpdatedAt: d(5.9)}},
	})
	s.AddRepo(Repo{Owner: "arc42", Name: "arc42.org-site", DefaultBranch: "main",
		Issues: []Issue{{Number: 12, Title: "Broken link on downloads page", Author: "visitor", CreatedAt: d(24 * 3), UpdatedAt: d(24 * 2),
			Comments: []Comment{{Author: "gernotstarke", At: d(24 * 2)}}}},
		Runs: []Run{{ID: 2002, Name: "deploy", Status: "completed", Conclusion: "failure", Branch: "main",
			URL: "https://github.com/arc42/arc42.org-site/actions/runs/2002", CreatedAt: d(1), UpdatedAt: d(0.9)}},
	})
	s.SetNotifications([]Notification{{ID: "n-1", Reason: "mention", SubjectTitle: "Would love your view on quality scenarios", SubjectType: "Issue",
		SubjectURL: "https://api.github.com/repos/someone/architecture-notes/issues/7", Repo: "someone/architecture-notes", UpdatedAt: d(3), Unread: true}})
}
