---
section: Fixed
---
Sign-in: with only the client secret in the environment, the start log now has one warning that `TASMOFLEET_OIDC_ISSUER` is missing because the secret locked it, and the Sign-in tab says so instead of "set by the environment"; the same for an issuer from the environment, which locks the client secret. `docs/configuration.md` states that both are set as variables or neither (#720, thanks @Noschvie)
