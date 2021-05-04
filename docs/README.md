# Telepresence GitBook

Just cloned?

First, make sure you have `node` 10.x or older; the GitBook CLI is
abandonware and uses old versions libraries that don't work on Node 12
or newer (see https://github.com/GitbookIO/gitbook-cli/issues/110).
If you do have too new of a `node`, you'll get errors like:

    /home/…/docs/node_modules/npm/node_modules/graceful-fs/polyfills.js:287
          if (cb) cb.apply(this, arguments)
                     ^

    TypeError: cb.apply is not a function
        at /home/…/docs/node_modules/npm/node_modules/graceful-fs/polyfills.js:287:18

Fortunately, Node 10.x is an LTS release code-named "Dubnium", and you
can quite-likely get it from your package manager; for example, on
Arch Linux and derivatives:

    sudo pacman -S nodejs-lts-dubnium

Once you have a suitably ancient `node` installed, set up GitBook to
get started.

    npm install

Now you can build the site, e.g., to push to a web server.

    npm run build

Still writing and editing? Run the server so you can preview as you go.

    npm start

### Redirects
There is a `redirects.json` file that holds the 301 redirects.

### Styling
Custom styles for the GitBook pages are in the styles/website.css file.
