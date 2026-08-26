# Registry research instructions

The `registry-research/` directory owns candidate and catalog research records.
Coordinate its ledger with the advisory research tooling in
`internal/research`; runtime registry validation belongs under
`internal/registrycheck`.

* Fetch current state of both the TarLink and tarlink-registry repositories before research, then use `./scripts/agent-context.sh` and `tarlink registry candidates --changed`.
* Consult `registry-research/candidates.yaml` as the durable decision ledger. Do not repeatedly reinvestigate an unchanged immutable release.
* A `RECHECK` result requires investigation; it is not approval. Inspection and provenance output are advisory evidence only; the official registry remains the trust boundary.
* Before implementing a security or artifact capability primarily to unblock candidates, run `tarlink registry blockers --capability <capability>` and report affected candidates, blockers removed, blockers remaining, and the number fully unlocked. If zero known candidates are fully unlocked, do not begin unless the capability is independently required or another concrete product requirement justifies it.
* Follow `docs/registry-research.md` for the research mechanics.
