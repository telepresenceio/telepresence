import Grid from '@material-ui/core/Grid';
import Paper from '@material-ui/core/Paper';
import Typography from '@material-ui/core/Typography';
import { makeStyles } from '@material-ui/core/styles';
import { Link as GatsbyLink } from 'gatsby';
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
              <GatsbyLink to="../howtos/personal-intercepts/">
                <b>Collaborating</b>
              </GatsbyLink>
            </Typography>
            <Typography variant="body2" component="p">
              Use personal intercepts to get specific requests when working with colleagues.
            </Typography>
          </Paper>
        </Grid>
        <Grid item xs={4}>
          <Paper variant="outlined" className={classes.paper}>
            <Typography variant="h6" component="h2">
              <GatsbyLink to="../howtos/outbound/">
                <b>Outbound Sessions</b>
              </GatsbyLink>
            </Typography>
            <Typography variant="body2" component="p">
              Control what your laptop can reach in the cluster while connected.
            </Typography>
          </Paper>
        </Grid>
        <Grid item xs={4}>
          <Paper variant="outlined" className={classes.paper}>
            <Typography variant="h6" component="h2">
              <GatsbyLink to="../docker/compose">
                <b>Telepresence for Docker Compose</b>
              </GatsbyLink>
            </Typography>
            <Typography variant="body2" component="p">
              Develop in a hybrid local/cluster environment using Telepresence for Docker Compose.
            </Typography>
          </Paper>
        </Grid>
      </Grid>
    </div>
  );
}
