# 6. Glossary

| Term | Meaning |
|------|---------|
| **Item** | One thing shown on the dashboard that came from an upstream service: a GitHub issue or pull request, or a Todoist task. Identified by its source and its upstream id. |
| **Source** | One upstream service zorgscope reads: GitHub, Plausible, Todoist. A source is enabled when its credential is present. |
| **Tile** | One rectangle on the dashboard, showing one source's items or figures, with its own freshness and error state. |
| **First seen** | The time of the refresh run that stored an item for the first time. Never changed afterwards while the item stays present. |
| **Last visit** | The time the user last declared the dashboard seen, either by "mark all seen" or by signing in for the first time. |
| **New** | An item whose first-seen time is later than the last-visit time. |
| **Refresh run** | One execution of "fetch all enabled sources and store the result", triggered by cron-job.org or by the user. Recorded with its outcome per source. |
| **Cold start** | The first request after the Fly Machine has been stopped; it includes starting the machine and the process. |
| **Fake sources** | A local HTTP server that answers like GitHub, Plausible and Todoist from fixture files, used for development and tests. |
