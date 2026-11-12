---
section: Fixed
---
The one-time backup layout migration now treats a file system without directory fsync (vboxsf, some 9p and SMB mounts) like the backups themselves do: it updates the row in the same run instead of leaving the entry for the next start, and logs the limitation once per directory; real I/O errors still leave the entry (#548)
