import React from 'react';
import PropTypes from 'prop-types';
import { makeStyles } from '@material-ui/core/styles';
import AppBar from '@material-ui/core/AppBar';
import Tabs from '@material-ui/core/Tabs';
import Tab from '@material-ui/core/Tab';
import Typography from '@material-ui/core/Typography';
import Box from '@material-ui/core/Box';
import Button from '@material-ui/core/Button';
import HubspotForm from 'react-hubspot-form';
import CodeBlock from '../../../../src/components/CodeBlock/CodeBlock'


function TabPanel(props) {
  const { children, value, index, ...other } = props;

  return (
    <div
      role="tabpanel"
      hidden={value !== index}
      id={`simple-tabpanel-${index}`}
      aria-labelledby={`simple-tab-${index}`}
      {...other}
    >
      {value === index && (
        <Box p={3}>
          <Typography>{children}</Typography>
        </Box>
      )}
    </div>
  );
}

TabPanel.propTypes = {
  children: PropTypes.node,
  index: PropTypes.any.isRequired,
  value: PropTypes.any.isRequired,
};

function a11yProps(index) {
  return {
    id: `simple-tab-${index}`,
    'aria-controls': `simple-tabpanel-${index}`,
  };
}

const useStyles = makeStyles((theme) => ({
  root: {
    flexGrow: 1,
    backgroundColor: 'transparent',

  },

}));

export default function SimpleTabs() {
  const classes = useStyles();
  const [value, setValue] = React.useState(0);

  const handleChange = (event, newValue) => {
    setValue(newValue);
  };

  return (
    <div className={classes.root}>
      <AppBar elevation={0} style={{ background: 'transparent', color: 'black', borderBottom: '1px solid #e8e8e8', }} position="static">
        <Tabs TabIndicatorProps={{ style: { background: '#AF5CF8' } }} value={value} onChange={handleChange} aria-label="simple tabs example">
          <Tab label="macOS" {...a11yProps(0)} style={{ minWidth: "10%", textTransform: 'none' }} />
          <Tab label="Linux" {...a11yProps(1)} style={{ minWidth: "10%", textTransform: 'none' }} />
          <Tab label="Windows" {...a11yProps(2)} style={{ minWidth: "10%", textTransform: 'none' }} />
        </Tabs>
      </AppBar>
      <TabPanel value={value} index={0}>

        {/*macOS install instructions*/}

      1. Download the latest binary(~60 MB):<br/>
      <code>sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/latest/telepresence -o /usr/local/bin/telepresence</code><br/>
      2. Make the binary executable:<br/>
      <code>sudo chmod a+x /usr/local/bin/telepresence</code>

      </TabPanel>

      <TabPanel value={value} index={1}>

        {/*Linux install instructions*/}
        
      1. Download the latest binary(~50 MB):<br/>
      <code>sudo curl -fL https://app.getambassador.io/download/tel2/linux/amd64/latest/telepresence -o /usr/local/bin/telepresence</code><br/>
      2. Make the binary executable:<br/>
      <code>sudo chmod a+x /usr/local/bin/telepresence</code>  

        </TabPanel>

      <TabPanel value={value} index={2}>

        {/*Windows install instructions*/}
        Telepresence for Windows is coming soon! Sign up here to notified when it is available.<br /><br />
        <HubspotForm
          portalId='485087'
          formId='2f542f1b-3da8-4319-8057-96fed78e4c26'
          onSubmit={() => console.log('Submit!')}
          onReady={(form) => console.log('Form ready!')}
          loading={<div>Loading...</div>}
        />

      </TabPanel>
    </div >
  );
}