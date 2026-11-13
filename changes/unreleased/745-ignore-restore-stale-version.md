---
section: Fixed
---
Add devices: Ignore, Ignore selected and Restore now carry the version of the device the page showed and are refused with "The form is out of date. Reload the page and try again." when the number has since been given to another device. Before, a page left open could hide (or restore) a device that appeared after it was loaded and that nobody had seen, because the numbers of deleted rows are reused (#745)
