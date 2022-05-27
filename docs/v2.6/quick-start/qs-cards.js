import Grid from '@material-ui/core/Grid';
import Paper from '@material-ui/core/Paper';
import Typography from '@material-ui/core/Typography';
import { makeStyles } from '@material-ui/core/styles';
import React from 'react';

const useStyles = makeStyles((theme) => ({
  root: {
    flexGrow: 1,
    textAlign: 'center',
    alignItem: 'stretch',
    padding: 0,
  },
  paper: {
    padding: theme.spacing(1),
    textAlign: 'center',
    color: 'black',
    height: '100%',
  },
}));

export default function CenteredGrid() {
  const classes = useStyles();

  return (
    <div className={classes.root}>
      <Grid container justify="center" alignItems="stretch" spacing={1}>
        <Grid item xs={4}>
          <Paper variant="outlined" className={classes.paper}>
            <Typography variant="h6" component="h2">
              <a href="../../howtos/preview-urls/">
                <b>Collaborating</b>
              </a>
            </Typography>
            <Typography variant="body2" component="p">
              Use preview URLS to collaborate with your colleagues and others
              outside of your organization.
            </Typography>
          </Paper>
        </Grid>
        <Grid item xs={4}>
          <Paper variant="outlined" className={classes.paper}>
            <Typography variant="h6" component="h2">
              <a href="../../howtos/outbound/">
                <b>Outbound Sessions</b>
              </a>
            </Typography>
            <Typography variant="body2" component="p">
              While connected to the cluster, your laptop can interact with
              services as if it was another pod in the cluster.
            </Typography>
          </Paper>
        </Grid>
        <Grid item xs={4}>
          <Paper variant="outlined" className={classes.paper}>
            <Typography variant="h6" component="h2">
              <a href="../../faqs/">
                <b>FAQs</b>
              </a>
            </Typography>
            <Typography variant="body2" component="p">
              Learn more about uses cases and the technical implementation of
              Telepresence.
            </Typography>
          </Paper>
        </Grid>
      </Grid>
    </div>
  );
}
