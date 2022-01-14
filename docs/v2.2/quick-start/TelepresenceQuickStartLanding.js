import React from 'react';

import Icon from '../../../../src/components/Icon';

import './telepresence-quickstart-landing.less';

/** @type React.FC<React.SVGProps<SVGSVGElement>> */
const RightArrow = (props) => (
  <svg {...props} viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
    <path d="M13.4 4.5A1.1 1.1 0 0 0 11.8 6l4.8 4.9h-12a1.1 1.1 0 0 0 0 2.2h12L11.8 18a1.1 1.1 0 0 0 1.6 1.5l6.7-6.7c.4-.4.4-1.2 0-1.6l-6.7-6.7Z" />
  </svg>
);

/** @type React.FC<{color: 'green'|'blue', withConnector: boolean}> */
const Box = ({ children, color = 'blue', withConnector = false }) => (
  <>
    {withConnector && (
      <div className="connector-container">
        <span />
      </div>
    )}
    <div className={`box-container ${color}`}>{children}</div>
  </>
);

const TelepresenceQuickStartLanding = () => (
  <div className="telepresence-quickstart-landing">
    <h1>
      <Icon name="telepresence-icon" /> Telepresence
    </h1>
    <p>
      Explore the use cases of Telepresence with a free remote Kubernetes
      cluster, or dive right in using your own.
    </p>

    <div className="demo-cluster-container">
      <div>
        <div className="main-title-container">
          <h2 className="title underlined">
            Use <strong>Our</strong> Free Demo Cluster
          </h2>
          <p>
            See how Telepresence works without having to mess with your
            production environments.
          </p>
        </div>
        <Box color="blue" withConnector>
          <p className="reading-time">6 minutes</p>
          <h2 className="title">Integration Testing</h2>
          <p>
            See how changes to a single service impact your entire application
            without having to run your entire app locally.
          </p>
          <a className="get-started blue" href="demo-node/">
            GET STARTED{' '}
            <RightArrow width={20} height={20} fill="currentColor" />
          </a>
        </Box>
        <Box color="blue" withConnector>
          <p className="reading-time">5 minutes</p>
          <h2 className="title">Fast code changes</h2>
          <p>
            Make changes to your service locally and see the results instantly,
            without waiting for containers to build.
          </p>
          <a className="get-started blue" href="go/">
            GET STARTED{' '}
            <RightArrow width={20} height={20} fill="currentColor" />
          </a>
        </Box>
      </div>
      <div>
        <div className="main-title-container">
          <h2 className="title underlined">
            Use <strong>Your</strong> Cluster
          </h2>
          <p>
            Understand how Telepresence fits in to your Kubernetes development
            workflow.
          </p>
        </div>
        <Box color="green" withConnector>
          <p className="reading-time">10 minutes</p>
          <h2 className="title">Intercept your service in your cluster</h2>
          <p>
            Query services only exposed in your cluster's network. Make changes
            and see them instantly in your K8s environment.
          </p>
          <a className="get-started green" href="../howtos/intercepts/">
            GET STARTED{' '}
            <RightArrow width={20} height={20} fill="currentColor" />
          </a>
        </Box>
      </div>
    </div>

    <div className="telepresence-video">
      <h2 className="telepresence-video-title">Watch the Demo</h2>
      <div className="video-section">
        <div>
          <p>
            See Telepresence in action in our <strong>3-minute</strong> demo
            video that you can share with your teammates.
          </p>
          <ul>
            <li>Instant feedback loops</li>
            <li>Infinite-scale development environments</li>
            <li>Access to your favorite local tools</li>
            <li>Easy collaborative development with teammates</li>
          </ul>
        </div>
        <div className="video-container">
          <iframe
            title="Telepresence Demo"
            src="https://www.youtube.com/embed/W_a3aErN3NU"
            frameBorder="0"
            allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture"
            allowFullScreen
          ></iframe>
        </div>
      </div>
    </div>
  </div>
);

export default TelepresenceQuickStartLanding;
