# Conventions for use in build-aux

Each `.mk` snippet starts with a reference header-comment of the
format:

    ```make
    # Copyright statement.
    #
    # A sentence or two introducing what this file does.
    #
    ## Section heading ##
    #  - type: item
    #  - type: item
    ## Section heading ##
    #  - item
    #  - item
    #
    # High-level prose documentation.
    ```

Always include (at a minimum) these 4 sections, even if their content
is `(none)`:

 - `## Eager inputs ##` (mostly variables) Eager-evaluated inputs that
   need to be defined *before* loading the snippet
 - `## Lazy inputs ##` (mostly variables) Lazy-evaluated inputs can be
   defined before *or* after loading the snippet
 - `## Outputs ##` (targets, variables, et c.)
 - `## common.mk targets ##`

If there are which targets from snippets other than `common.mk` that
it hooks in to, a section for each of those snippets should exist too.

The most common reason for an input to be eager is if it is listed as
a dependency of a target defined in the `.mk` file.

Bullet points under "Eager inputs", "Lazy inputs", or "Outputs" should
be of the format "Type: thing [extra info]", optionally with the ":"
aligned between rows.  Types currently used are:

 - `Target(s)` (only for use as an output): Indicates that the snippet
   defines rules to create the specified file(s).  Prefer to list each
   target separately; only use the plural `Targets` when listing an
   expression that govers a large range of actual files.
 - `.PHONY Target(s)` (only for use as an output): Like `Target`, but
   is declared as `.PHONY`; a file with that name is never actually
   created.
 - `File` (only for use as an input): Indicates that this file is
   parsed by the Makefile snippet.  Note that it should not be a file
   described by a Target, since it needs to be there at
   Makefile-parse-time.
 - `Variable`: The meaning is obvious.
   * For inputs: If there is a default value, the listing should
     include `?= value` for a possibly pseudo-code or comment `value`
     documenting what the default value is.
   * For outputs: The listing should include `= value` or `?= value`
     for a possibly pseudo-code or comment `value` documenting what
     the variable is set to contain.  The `=` should be `?=` as
     appropriate, to document whether it can be overridden by the
     caller/user.
   * If a `?=` variable is listed in "Outputs", consider also listing
     it in "inputs"; if an input gets a default value set, consider
     also listing it in "Outputs".
     - `NAME` in `go-mod.mk`.  It isn't listed as an an input, because
       it isn't one "in spirit".
     - `KUBECONFIG` in `kubernaut-ui.mk` is ":=", but it is listed as
       an input anyway; it's one "in spirit"; it's just that it must
       be overridden more forcefully than with an environment
       variable.
     - "Executables" (below) are set with `?=`, but don't list them as
       "inputs".
 - `Function`: (only for use as an output) This is a special case of a
   Variable that defines a function to be called with `$(call
   funcname,args)`.  Should not be listed as an input; if you depend
   on a function, just go ahead and include the snippet that defines
   it.
 - `Executable` (only for use as an output): An "Executable" output is
   a special-case of both a "Target" output and a "Variable"
   input/output.  An "Executable" output has the following attributes:
   * It is exposed as a variable that stores an absolute path to an
     executable file (this makes it safe to list as a dependency).
   * That variable can be overridden using an environment variable
     (that is: it is set with `?=`).
   * It is NOT guaranteed to already exist (you may need to depend on
     it), but a rule to create it is guaranteed be there it it doesn't
     already exist.
   * Overriding it using an environment variable will not affect the
     rule to create the standard version, or cleanup rules to remove
     it; those targets do not obey the environment variable.
