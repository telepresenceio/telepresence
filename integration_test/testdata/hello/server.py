import os
from flask import Flask

PORT = 8000
MESSAGE = "Hello from intercepted {} with id {}!\n".format(os.environ["TELEPRESENCE_CONTAINER"], os.environ["TELEPRESENCE_INTERCEPT_ID"])

app = Flask(__name__)


@app.route("/")
def root():
    result = MESSAGE.encode("utf-8")
    return result


if __name__ == "__main__":
    app.run(debug=True, host="0.0.0.0", port=PORT)
