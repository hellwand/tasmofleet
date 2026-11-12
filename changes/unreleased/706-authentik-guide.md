---
section: Fixed
---
The Authentik guide printed the sign-in variables without the `TASMOFLEET_` prefix for the container configuration, where a name without the prefix is ignored without a message (the short names are valid only in `/data/tasmofleet.env`); it now uses the prefixed names, says that issuer and client secret must be set in the same place and that a setting comes from the page or from the container configuration, names the "Sign-in" tab, and marks the application icon as an optional Authentik-side step that tasmofleet neither sends nor reads (#706, thanks @Noschvie)
