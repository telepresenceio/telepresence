const core = require('@actions/core');
const github = require('@actions/github');
const fs = require('fs');

try {
  // inputs are defined in action metadata file
  const distribution = core.getInput('distribution');
  const version = core.getInput('version');
  const kubeconfig = core.getInput('kubeconfig');
  console.log(`Creating ${distribution} ${version} and writing kubeconfig to file: ${kubeconfig}!`);
  fs.writeFile(kubeconfig, `Mock kubeconfig file for ${distribution} ${version}.\n`, err => {
    if (err) {
      core.setFailed(`${err}`);
    }
  });
  // Get the JSON webhook payload for the event that triggered the workflow
  //const payload = JSON.stringify(github.context.payload, undefined, 2)
  //console.log(`The event payload: ${payload}`);
} catch (error) {
  core.setFailed(error.message);
}
