---
section: Fixed
---
Add devices: "Show ignored" now counts and lists a dismissed address change or conflict too, in its own section (Address changed or Conflicts) with an Ignored badge and Restore as its only action, and Restore puts it back there. Before, such a dismissal could not be undone: the count skipped it, and the device keeps announcing the same data, so the 30-day expiry never removed it (#714)
