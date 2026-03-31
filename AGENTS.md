# AGENTS

## Lessons Learned

- Prefer short to medium-length variable and function names in production code, with clear, high-value meaning.
- Avoid overly long production identifiers when a shorter precise name communicates the same intent.
- Favor names that describe the real responsibility of the code, not every implementation detail.
- In tests, longer and more explicit names are acceptable when they improve readability and make the scenario clearer.
- Example: prefer names like `checkpointInterval` or `shouldCheckpoint` in production code over verbose names that repeat surrounding context unnecessarily.
- We are in the early stages of development, so we are not concerned about migration of databases or needing to support older version of databases or similar. We can break compatibility with previous versions if needed. That means we consider every install as a fresh install and not a update.