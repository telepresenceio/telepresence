var url = new URL(window.location.href);
var utmSource = url.searchParams.get('utm_source');
var datawireBreadcrumb = document.getElementById('datawire-breadcrumb');

if (utmSource === 'datawire-docs') {
  datawireBreadcrumb.style.display = 'block';
}