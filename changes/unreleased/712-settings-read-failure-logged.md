---
section: Changed
---
A failed Settings read of a device now writes one warning to the log with the device id, the query that failed and the reason as a short class (timeout, unreachable, auth and so on); the page text is unchanged and the line never holds a setting value or a credential (#712)
