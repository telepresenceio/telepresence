import queryString from 'query-string';
import React, { useEffect, useState } from 'react';

import Embed from '../../../../src/components/Embed';
import Icon from '../../../../../src/components/Icon';
import Link from '../../../../../src/components/Link';

import './telepresence-quickstart-landing.less';

/** @type React.FC<React.SVGProps<SVGSVGElement>> */
const RightArrow = (props) => (
  <svg {...props} viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
    <path d="M13.4 4.5A1.1 1.1 0 0 0 11.8 6l4.8 4.9h-12a1.1 1.1 0 0 0 0 2.2h12L11.8 18a1.1 1.1 0 0 0 1.6 1.5l6.7-6.7c.4-.4.4-1.2 0-1.6l-6.7-6.7Z" />
  </svg>
);

const TelepresenceQuickStartLanding = () => {

  return (
    <div className="telepresence-quickstart-landing">
      <h1>
        <Icon name="telepresence-icon" /> Telepresence OSS
      </h1>
      <p>
        Set up your ideal development environment for Kubernetes in seconds.
        Accelerate your inner development loop with hot reload using your
        existing IDE, and workflow.
      </p>

      <div className="demo-cluster-container">
        <div>
          <div className="box-container">
            <h2>
              <strong>Install Telepresence and connect to your Kubernetes workloads.</strong>
            </h2>
            <Link to="/docs/telepresence-oss/latest/install" className="docs__button-secondary blue">
              Get Started
            </Link>
          </div>
        </div>
      </div>

      <div className="telepresence-video">
          <div>
            <h2 className="telepresence-video-title">
              What Can Telepresence Do for You?
            </h2>
            <p>Telepresence gives Kubernetes application developers:</p>
            <ul>
              <li>Make changes on the fly and see them reflected when interacting with your remote Kubernetes environment, this is just like hot reloading, but it works across both local and remote environments.</li>
              <li>Query services and microservice APIs that are only accessible in your remote cluster's network.</li>
              <li>Set breakpoints in your IDE and re-route remote traffic to your local machine to investigate bugs with realistic user traffic and API calls.</li>
            </ul>
            <Link className="learn-more blue" to="/products/telepresence">
              LEARN MORE{' '}
              <RightArrow width={24} height={24} fill="currentColor" />
            </Link>
          </div>
      </div>
    </div>
  );
};

export default TelepresenceQuickStartLanding;
