The point of this repo is to give consistent and reliable Kubernetes deployments distributed across multiple machines, with a hostapp server that connects to multiple clusters, with a user friendly web UI, in which the user can join/manage clusters, deploy & inspect docker images (with a code-server running in the provided image), and database management.

IMPORTANT: Multi-line quoted commands usually stall the CLI - avoid them if possible.

Any new features or configurations must work by default, but can be customized via environment variables or other configurations as appropriate. This project only has a production flow as this is a developer tool. Everything should work out of the box with sensible defaults.

Do NOT create 'dev' or 'local' modes or configurations. Everything should work the same in all environments.

Prefer modularity and composability over monoliths. Each component should do one thing well and be replaceable.

Always ensure everything the user asks to be done is actually done, even if it requires multiple steps, complex logic or terminal commands. If the user asks for a file to be created, create it with the correct content. If the user asks for code to be modified, modify it correctly. If a file needs to be deleted, delete it. Never leave the user with an incomplete task or half-finished code.

Before asking the user to choose from an option, automatically go with the simplest option and keep going until the problem is solved.

ALWAYS update API.md, architecture.md and DEPLOYMENT.md with any relevant changes without duplicating information.

Always update planning docs with progress on tasks.

Do not leave deprecated code or comments in the codebase. Remove any code that is no longer needed.

Use /tmp for temporary files, never the project directory.