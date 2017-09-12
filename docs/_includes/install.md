<script>
$(document).ready(function() {
  var button = $(".toggleInstall.{{ include.location }}");
  button.click(function() {
    $(".install-telepresence.{{ include.location }}").toggle();
    if (button.html() == "Show") {
        button.html("Hide");
    } else {
        button.html("Show");
    }
  });
});
</script>

### Install Telepresence with Homebrew/apt/dnf
#### **<a class="button toggleInstall {{ include.location }}" data-location="{{ include.location }}">Show</a>**

<div class="install-telepresence {{ include.location }}" style="display: none;" markdown="1">

You will need the following available on your machine:

* `{{ include.command }}` command line tool (here's the [installation instructions]({{ include.install }})).
* Access to your {{ include.cluster }} cluster, with local credentials on your machine.
  You can test this by running `{{ include.command }} get pod` - if this works you're all set.

{% include install-specific.md %}

</div>
