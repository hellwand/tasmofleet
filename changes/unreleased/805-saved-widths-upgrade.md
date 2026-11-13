---
section: Upgrade notes
---
A column layout of the Devices list saved with an earlier version keeps its column proportions but not its total width, which could hold the stretch of the window it was saved in: it now fits the window until its columns reach their minimum width and scrolls only below that. The next change to the layout saves it in the new form, as it looks. Reset to default brings back the default widths. No database migration (#805)
