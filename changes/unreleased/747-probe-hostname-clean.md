---
section: Security
---
The hostname a device reports is now cleaned like the rest of its text before it is stored (control and invisible characters removed, at most 100 characters). Before, a device, or anything answering at its address, could store a hostname with line breaks, invisible or direction-changing characters, or of any length, which then showed on its pages. The hostname listed in the message about a failed scheduled backup is cleaned as well, also for hostnames stored by older versions (#747)
