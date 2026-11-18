## Environment & Stack

- **The sandbox is the machine.** Build, run, and test inside your own sandbox, the way the project's `AGENTS.md` and its local deploy doc say. Docker works there; a remote box, a VM or a cloud is never reached by a run unless the ticket says so and the run was given the access.
- **GCP is the only cloud.** A design that needs a cloud uses Google Cloud; never suggest another provider. Project ID, region, Artifact Registry repository, service name, service account and whether a service is public come from the project's `docs/deploy/gcp.md` or the ticket; one that is missing is never invented: record the gap with `decide` and carry on with what does not need it.
- **Backend: Go** (`golang` skill): `cmd/<app>/` + `internal/`, `log/slog` JSON logging.
- **Frontend: TanStack Start + TypeScript + Tailwind**, fully contained in one `web/` or `ui/` directory inside the Go project (`frontend`, `typescript` skills). Shipped JS runs on plain V8: no Node-only APIs outside the server entry.
- **Build and image mechanics** (local test first, amd64 images, Artifact Registry paths tagged by git SHA) are the `docker` and `cloud-run-deploy` skills'. A run builds and tests an image; it never pushes one to a registry or deploys it.
- **Compute:** Cloud Run first (`cloud-run-deploy` skill); GKE (`gke-basics`) or Compute Engine when Cloud Run does not fit. Infrastructure as code with Terraform (`terraform` skill) when it is more than one service.
- **Secrets** reach Cloud Run through Secret Manager (`--set-secrets`), never `--set-env-vars`.
