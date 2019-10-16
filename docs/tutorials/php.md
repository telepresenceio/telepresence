# Local development with PHP
*Author: Solomon Roberts ([@BadgerOps](https://twitter.com/BadgerOps))*

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

##### Note: this is heavily influenced by the awesome [Local Java development](https://www.telepresence.io/tutorials/java) doc by Cesar Tron-Lozai ([@CesarTronLozai](https://twitter.com/cesarTronLozai))

## PHP 

`Telepresence` can help you speed up your development process for any technology, as long as you deploy your service as a Docker image into a Kubernetes container.

In this tutorial we will focus on how to setup a local development environment for a (micro)-service `Bar` written in PHP.

This is is very useful if your application is formed of many such services which cannot run on a single development machine. In which case it's easy to setup a separate Kubernetes cluster dedicated for development.

`Telepresence` will help us locally develop our service `Bar` as if it was still inside the Kubernetes cluster. It's a win-win!!

## Architecture

The idea is quite simple, `Telepresence` will start a Docker container on your local machine, remove the running pod for `Bar` and replace it with a two-way proxy to your local docker container.

If other services in your cluster want to talk to `Bar`, they'll get redirected to your local process. If your local process wants to talk to any other services running in your cluster, `Telepresence` will redirect the calls to your cluster.
It will also maintain all the environment variables defined in your deployment. It's magical.

In order to run our PHP application in a local Docker container, we can simply start a container which has PHP and Apache installed, mount the source directory to our code, and start coding!

In this tutorial we will be using PHP 7.2 and an Apache based container, this could also work with PHP-FPM + Nginx, you'd just need to adjust the default xdebug port of 9000 because that will conflict with PHP-FPM.

## Building inside a docker container

As mentioned above, the goal is to compile and run our code inside a Docker container which `Telepresence` can use to replace the pod running in your cluster.

Let's build the command step by step.

* `telepresence` Runs `Telepresence`!
* `--container-to-host 9000` Forward port 9000 inside the container back to your computer's localhost on port 9000. This allows us to use xdebug for debugging and stepping through code.
* `--new-deployment bar` Create a new deployment called `bar` - you could also use ``-swap-deployment bar` if you want to test against an existing configured cluster.
* `--docker-run` Tells `Telepresence` to run a Docker containers
* `--rm` Tells Docker to discard our image when it terminates (no need to clutter your computer)
* `-v$(pwd):/var/www/html` Mounts the current directory (result of the `pwd` command) into a `/var/www/html` folder inside the Docker container. This is where your source code will be; and mounted in the container where Apache is configured to look for php/html files to serve. 
  * you could also specify the fully qualified path to your code repo if you don't want to execute this command in your code directory.
* `-p 8080:80` Forward Apache on `80` to http://localhost:8080 so you can hit your web service with your browser on your local computer.
* `myapp:01` The container that you are wanting to execute with the Telepresence command.

And that's it! You can easily create a `telepresence.sh` file in the root of your project with the following:

> telepresence.sh
> ```bash
> telepresence --container-to-host 9000 --new-deployment bar --docker-run --rm -v$(pwd):/var/www/html -p 8080:80 myapp:01
>```


## Example of how to test Telepresence + PHP

The key piece here is the `--container-to-host 9000` (Note, if you're using php-fpm, then you'll want to forward a different port for xdebug, since php-fpm uses 9000)

This creates a reverse connection from the container that your code is executing in back to the host machine so your xdebug listener can receive the connection.

Create an `index.php` with the following content:

```php
<html>  
 <head>
  <title>PHP Telepresence Demo</title>
 </head>
 <body>
 <?php echo '<p>Hello World!</p>'; ?>
 <?php phpinfo(); ?>
 </body>
</html>
```

Next, create a Dockerfile with the following contents:

```dockerfile
FROM php:7.2-apache  
RUN pecl install xdebug-2.6.0  
RUN docker-php-ext-enable xdebug  
RUN echo "xdebug.remote_enable=1" >> /usr/local/etc/php/php.ini && \  
    echo "xdebug.remote_host=localhost" >> /usr/local/etc/php/php.ini && \
    echo "xdebug.remote_port=9000" >> /usr/local/etc/php/php.ini && \ 
    echo "xdebug.remote_log=/var/log/xdebug.log" >> /usr/local/etc/php/php.ini 


COPY ./index.php /var/www/html  
WORKDIR /var/www/html
```

Next, you'll build this container:

```bash
docker build -t myapp:01 .
```

Finally, execute your Telepresence command we built earlier:

```bash
telepresence --container-to-host 9000 --new-deployment bar --docker-run --rm -v$(pwd):/var/www/html -p 8080:80 myapp:01
```

And here is what example output should be (Using Telepresence build 0.102 in Oct of 2019 )

```
telepresence --container-to-host 9000 --verbose --new-deployment tele-test --docker-run -p 8080:80 -v $(pwd):/var/www/html myapp:01  
T: How Telepresence uses sudo: https://www.telepresence.io/reference/install#dependencies  
T: Invoking sudo. Please enter your sudo password.  
Password:  
T: Volumes are rooted at $TELEPRESENCE_ROOT. See https://telepresence.io/howto/volumes.html for details.  
T: Starting network proxy to cluster using new Deployment tele-test

T: No traffic is being forwarded from the remote Deployment to your local machine. You can use the --expose option to specify which ports you want to forward.

T: Forwarding container port 9000 to host port 9000.  
T: Setup complete. Launching your container.  
AH00558: apache2: Could not reliably determine the server's fully qualified domain name, using 172.17.0.2. Set the 'ServerName' directive globally to suppress this message  
AH00558: apache2: Could not reliably determine the server's fully qualified domain name, using 172.17.0.2. Set the 'ServerName' directive globally to suppress this message  
[Thu Oct 03 17:04:35.421678 2019] [mpm_prefork:notice] [pid 7] AH00163: Apache/2.4.38 (Debian) PHP/7.2.23 configured -- resuming normal operations
[Thu Oct 03 17:04:35.422032 2019] [core:notice] [pid 7] AH00094: Command line: 'apache2 -D FOREGROUND'
```

We can see that the container port 9000 is forwarded to our host port 9000, and Apache launches. 

I use PHPSTorm for PHP development, so I used the [PHPStorm xdebug guide](https://www.jetbrains.com/help/phpstorm/configuring-xdebug.html) to configure my browser (with the xdebug extension) and debugger in PHPStorm. We've also set up IntelliJ IDEA to debug with the same steps. 

You should then be able to turn on your debug listener in your IDE, set a breakpoint, and navigate to `http://localhost` in your browser to load the code and hit your breakpoint!

This tutorial adapted from a very basic example in [this blog post](https://blog.badgerops.net/2019/10/03/debugging-a-php-app-in-kubernetes-using-telepresence-io/) - if you have any problems or questions, feel free to join the telepresence slack or reach out to [@BadgerOps](https://twitter.com/BadgerOps) on twitter.