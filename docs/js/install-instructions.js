$(document).ready(function() {
    $('body').on('click', '.toggleInstall', function() {
        $(".install-telepresence." + $(this).data('location')).toggle();
        if ($(this).html() == "Show") {
            $(this).html("Hide");
        } else {
            $(this).html("Show");
        }
    });

    var clipboard = new Clipboard('.copy-to-clipboard');
    clipboard.on('success', function(e) {
        $(e.trigger).text('Copied');
        e.clearSelection();
    });

    window.mermaid.init(undefined, document.querySelectorAll('.mermaid'));

    gitbook.events.bind("page.change", function() {
       window.mermaid.init(undefined, document.querySelectorAll('.mermaid'));
    });
});