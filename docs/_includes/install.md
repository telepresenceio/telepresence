<script src="https://code.jquery.com/jquery-3.2.1.slim.min.js"></script>
<script>
$(document).ready(function() {
  $("#toggleinstall").click(function() {
    $("#install-telepresence").toggle();
    var button = $("#toggleinstall");
    if (button.html() == "Show") {
        button.html("Hide");
    } else {
        button.html("Show");
    }
  });
});
</script>

### Install Telepresence with Homebrew/apt/dnf
#### **<a class="button" id="toggleinstall">Show</a>**

<div id="install-telepresence" style="display: none;" markdown="1">

You will need the following available on your machine:

* `{{ include.command }}` command line tool (here's the [installation instructions]({{ include.install }})).
* Access to your {{ include.cluster }} cluster, with local credentials on your machine.
  You can test this by running `{{ include.command }} get pod` - if this works you're all set.

{% include install-specific.md %}

</div>
