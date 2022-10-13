const core = require('@actions/core');
const github = require('@actions/github');

try {
  // inputs are defined in action metadata file
  const distribution = core.getInput('distribution');
  const version = core.getInput('version');
  console.log(`Deleting ${distribution} ${version}!`);
  // Get the JSON webhook payload for the event that triggered the workflow
  //const payload = JSON.stringify(github.context.payload, undefined, 2)
  //console.log(`The event payload: ${payload}`);
} catch (error) {
  core.setFailed(error.message);
}
