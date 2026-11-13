---
section: Security
---
The Unicode line and paragraph separators (U+2028, U+2029) are now removed from device names and hostnames like the other control and invisible characters. Before, a name or hostname containing one was kept and could show as a second line in the message about a failed scheduled backup; discovered devices were affected in the same way (#755)
