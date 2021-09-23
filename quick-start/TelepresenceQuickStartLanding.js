import React from 'react';
import './telepresence-quickstart-landing.less';

/** @type React.FC<React.SVGProps<SVGSVGElement>> */
const InterceptSVG = (props) => (
  <svg {...props} fill="none" xmlns="http://www.w3.org/2000/svg">
    <g opacity=".2" fill="#06F">
      <path d="M14.2 4.8a5.7 5.7 0 0 0-5.9 5.6v24c0 3 2.6 5.6 5.9 5.6 3.2 0 5.8-2.5 5.8-5.6v-24c0-3-2.6-5.6-5.8-5.6ZM29.2 4.8a5.7 5.7 0 0 0-5.9 5.6v24c0 3 2.6 5.6 5.9 5.6 3.2 0 5.8-2.5 5.8-5.6v-24c0-3-2.6-5.6-5.8-5.6Z" />
    </g>
    <path d="M23.4 1.6c.5.4.5 1.2 0 1.6L20.5 6h4.8a6 6 0 0 1 4.2 1.7c1.1 1 1.7 2.5 1.7 4v14.9c2.2.5 3.8 2.4 3.8 4.6 0 2.7-2.2 4.8-5 4.8s-5-2.1-5-4.8c0-2.2 1.6-4.1 3.7-4.6V11.7c0-.9-.3-1.7-1-2.3-.6-.7-1.5-1-2.4-1h-4.8l2.9 2.8c.5.4.5 1.2 0 1.6-.5.5-1.3.5-1.8 0l-5-4.8c-.5-.4-.5-1.2 0-1.6l5-4.8c.5-.5 1.3-.5 1.8 0ZM30 28.8c-1.4 0-2.5 1-2.5 2.4 0 1.3 1.1 2.4 2.5 2.4s2.5-1 2.5-2.4c0-1.3-1.1-2.4-2.5-2.4Z" fill="#06F" />
    <path d="M11.3 11.8A4.8 4.8 0 0 0 15 7.2c0-2.7-2.2-4.8-5-4.8S5 4.5 5 7.2c0 2.2 1.6 4.1 3.7 4.6v14.8A4.8 4.8 0 0 0 5 31.2c0 2.7 2.2 4.8 5 4.8s5-2.1 5-4.8c0-2.2-1.6-4.1-3.8-4.6V11.8ZM10 9.6a1.3 1.3 0 0 0-.2 0c-1.3 0-2.4-1-2.4-2.4 0-1.3 1.1-2.4 2.5-2.4s2.5 1 2.5 2.4c0 1.3-1 2.4-2.4 2.4ZM7.5 31.2c0-1.3 1.1-2.4 2.5-2.4s2.5 1 2.5 2.4c0 1.3-1.1 2.4-2.5 2.4s-2.5-1-2.5-2.4Z" fill="#003380" />
    <ellipse cx="30" cy="31.2" rx="2.5" ry="2.4" fill="#00C05B" />
  </svg>
);

/** @type React.FC<React.SVGProps<SVGSVGElement>> */
const RightArrow = (props) => (
  <svg {...props} viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
    <path d="M13.4 4.5A1.1 1.1 0 0 0 11.8 6l4.8 4.9h-12a1.1 1.1 0 0 0 0 2.2h12L11.8 18a1.1 1.1 0 0 0 1.6 1.5l6.7-6.7c.4-.4.4-1.2 0-1.6l-6.7-6.7Z" />
  </svg>
)

/** @type React.FC<{color: 'green'|'blue', withConnector: boolean}> */
const Box = ({ children, color = 'blue', withConnector = false }) => {
  return (
    <>
      {withConnector && (
        <div className="connector-container"><span /></div>
      )}
      <div className={`box-container ${color}`}>
        {children}
      </div>
    </>
  );
};

const TelepresenceQuickStartLanding = () => (
  <div className="telepresence-quickstart-landing">
    <h1>
      <InterceptSVG width={40} height={40} /> Telepresence
    </h1>
    <p>
      Explore the use cases of Telepresence with a free remote Kubernetes cluster, or dive right in using your own.
    </p>

    <div className="demo-cluster-container">
      <div>
        <div className="main-title-container">
          <h2 className="title underlined">
            Use <strong>Our</strong> Free Demo Cluster
          </h2>
          <p>See how Telepresence works without having to mess with your production environments</p>
        </div>
        <Box color="blue" withConnector>
          <p className="reading-time">
            6 minutes
          </p>
          <h2 className="title">Integration Testing</h2>
          <p>
            Lorem ipsum dolor sit amet consectetur adipisicing elit. Fugit inventore exercitationem aut deleniti, voluptate dicta ipsa provident minima tenetur, magni quasi ipsam sequi aliquam. Assumenda, iure. Sunt soluta architecto deleniti?
          </p>
          <a className="get-started blue" href="demo-node/">GET STARTED <RightArrow width={20} height={20} fill="currentColor" /></a>
        </Box>
        <Box color="blue" withConnector>
          <p className="reading-time">
            6 minutes
          </p>
          <h2 className="title">Safe code changes</h2>
          <p>
            Lorem ipsum dolor sit amet consectetur adipisicing elit. Fugit inventore exercitationem aut deleniti, voluptate dicta ipsa provident minima tenetur, magni quasi ipsam sequi aliquam. Assumenda, iure. Sunt soluta architecto deleniti?
          </p>
          <a className="get-started blue" href="go/">GET STARTED <RightArrow width={20} height={20} fill="currentColor" /></a>
        </Box>
      </div>
      <div>
        <div className="main-title-container">
          <h2 className="title underlined">
            Use <strong>Your</strong> Cluster
          </h2>
          <p>Understand how Telepresence fits in to your Kubernetes development workflow</p>
        </div>
        <Box color="green" withConnector>
          <p className="reading-time">
            10 minutes
          </p>
          <h2 className="title">
            Intercept your service in your cluster
          </h2>
          <p>
            Lorem ipsum dolor sit amet consectetur adipisicing elit. Fugit inventore exercitationem aut deleniti, voluptate dicta ipsa provident minima tenetur, magni quasi ipsam sequi aliquam. Assumenda, iure. Sunt soluta architecto deleniti?
          </p>
          <a className="get-started green" href="../howtos/intercepts/">GET STARTED <RightArrow width={20} height={20} fill="currentColor" /></a>
        </Box>
      </div>
    </div>

    <div className="telepresence-video">
      <h2>
        Watch the Demo
      </h2>
      <div className="video-wrapper">
        <div className="description">
          <p>
            See Telepresence in action in our <strong>3-minute</strong> demo video that you can share with your teammates.
          </p>
          <ul>
            <li>Instant feedback loops</li>
            <li>Infinite-scale development environments</li>
            <li>Access to your favorite local tools</li>
            <li>Easy collaborative development with teammates</li>
          </ul>
        </div>
        <div className="video-container">
          <iframe className="video" title="Telepresence Demo" src="https://www.youtube.com/embed/W_a3aErN3NU" frameBorder="0" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture" allowFullScreen></iframe>
        </div>
      </div>
    </div>
  </div>
);

export default TelepresenceQuickStartLanding;
