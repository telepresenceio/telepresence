

---
Make sure every source file you touch has the standard license header.

Before landing, add a changelog entry as a file `newsfragments/issue_number.type`, where `type` is one of `incompat`, `feature`, `bugfix`, or `misc`. Preview the changelog with `virtualenv/bin/towncrier --draft`. 

E.g., `532.bugfix` would contain the text "Telepresence should no longer get confused looking for the route to the host when using the container method."
