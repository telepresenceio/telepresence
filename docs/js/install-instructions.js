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

    var path = null;

    setInterval(function() {
        if (path !== window.location.pathname) {
            path = window.location.pathname;
            window.mermaid.init(undefined, document.querySelectorAll('.lang-mermaid'))
        }
    }, 1000);
});