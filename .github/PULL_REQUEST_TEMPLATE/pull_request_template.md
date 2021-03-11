

---

## Helpful information

- CircleCI will run the linters and unit tests on your PR.
  - The results of that CI run will be listed as "check-local."
  - You can run the same tests yourself:

    ```shell
    make lint check-local
    ```

  - The other checks for your PR will succeed immediately without testing anything. They rely on Datawire secrets to push images or talk to a Kubernetes cluster, so they are disabled for forked-repo PRs.

- Please include a changelog entry as a file `newsfragments/issue_number.type`.
  - `type` is one of `incompat`, `feature`, `bugfix`, or `misc`
  - E.g., `1003.bugfix` would contain the text
    > Attaching a debugger to the process running under Telepresence no longer causes the session to end.
  - You can preview the changelog with

    ```shell
    virtualenv/bin/towncrier --draft
    ```

- Please delete this pre-filled text ("Helpful information"). Note that you can edit the description after submitting the PR if you prefer.

Thank you for your contribution!
