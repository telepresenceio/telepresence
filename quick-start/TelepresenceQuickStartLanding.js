import React, { Component } from "react";
import './telepresence-quickstart-landing.less';

class TelepresenceQuickStartLanding extends Component {
      render() {
    return (
        <div className="telepresence-quickstart-landing">
            <h1>
                <svg width="40" height="40" viewBox="0 0 40 40" fill="none" xmlns="http://www.w3.org/2000/svg">
                <g opacity="0.2">
                <path d="M14.1665 4.79999C10.9448 4.79999 8.33313 7.30719 8.33313 10.4V34.4C8.33313 37.4928 10.9448 40 14.1665 40C17.3881 40 19.9998 37.4928 19.9998 34.4V10.4C19.9998 7.30719 17.3881 4.79999 14.1665 4.79999Z" fill="#0066FF"/>
                <path d="M29.1665 4.79999C25.9448 4.79999 23.3331 7.30719 23.3331 10.4V34.4C23.3331 37.4928 25.9448 40 29.1665 40C32.3881 40 34.9998 37.4928 34.9998 34.4V10.4C34.9998 7.30719 32.3881 4.79999 29.1665 4.79999Z" fill="#0066FF"/>
                </g>
                <path fillRule="evenodd" clipRule="evenodd" d="M23.3838 1.55039C23.872 2.01901 23.872 2.77881 23.3838 3.24744L20.5173 5.9993H25.3124C26.8871 5.9993 28.3974 6.59984 29.5109 7.6688C30.6244 8.73775 31.2499 10.1876 31.2499 11.6993V26.5519C33.4064 27.0848 34.9999 28.9641 34.9999 31.2007C34.9999 33.8516 32.7613 36.0007 29.9999 36.0007C27.2385 36.0007 24.9999 33.8516 24.9999 31.2007C24.9999 28.9641 26.5934 27.0847 28.7499 26.5519V11.6993C28.7499 10.8241 28.3878 9.98472 27.7431 9.36585C27.0984 8.74698 26.2241 8.3993 25.3124 8.3993H20.5181L23.3838 11.1504C23.872 11.619 23.872 12.3788 23.3838 12.8474C22.8957 13.3161 22.1042 13.3161 21.616 12.8474L16.616 8.04744C16.1279 7.57881 16.1279 6.81901 16.616 6.35038L21.616 1.55039C22.1042 1.08176 22.8957 1.08176 23.3838 1.55039ZM29.9999 28.8007C28.6192 28.8007 27.4999 29.8752 27.4999 31.2007C27.4999 32.5262 28.6192 33.6007 29.9999 33.6007C31.3806 33.6007 32.4999 32.5262 32.4999 31.2007C32.4999 29.8752 31.3806 28.8007 29.9999 28.8007Z" fill="#0066FF"/>
                <path fillRule="evenodd" clipRule="evenodd" d="M11.25 11.8492C13.4065 11.3163 14.9999 9.43704 14.9999 7.20045C14.9999 4.54948 12.7613 2.40045 9.99992 2.40045C7.2385 2.40045 4.99992 4.54948 4.99992 7.20045C4.99992 9.43707 6.59342 11.3164 8.74998 11.8492V26.5519C6.59342 27.0847 4.99992 28.964 4.99992 31.2006C4.99992 33.8516 7.2385 36.0006 9.99992 36.0006C12.7613 36.0006 14.9999 33.8516 14.9999 31.2006C14.9999 28.9641 13.4065 27.0847 11.25 26.5519V11.8492ZM10.0557 9.59986C10.0372 9.59909 10.0186 9.59869 9.99998 9.59869C9.9813 9.59869 9.96272 9.59909 9.94425 9.59986C8.58925 9.57144 7.49992 8.50807 7.49992 7.20045C7.49992 5.87497 8.61921 4.80045 9.99992 4.80045C11.3806 4.80045 12.4999 5.87497 12.4999 7.20045C12.4999 8.50805 11.4106 9.5714 10.0557 9.59986ZM7.49992 31.2006C7.49992 29.8752 8.61921 28.8006 9.99992 28.8006C11.3806 28.8006 12.4999 29.8752 12.4999 31.2006C12.4999 32.5261 11.3806 33.6006 9.99992 33.6006C8.61921 33.6006 7.49992 32.5261 7.49992 31.2006Z" fill="#003380"/>
                <ellipse cx="30" cy="31.2001" rx="2.5" ry="2.4" fill="#00C05B"/>
                </svg>
                Telepresence Quick Start
            </h1>
            <p>
                Code and test microservices <strong>locally</strong> against a <strong>remote</strong> Kubernetes cluster.
            </p>
            <div className="telepresence-choice-wrapper">
                <div className="telepresence-choice">
                    <h2>
                      <mark className="highlight-mark">
                        New
                      </mark>
                        to Kubernetes?
                    </h2>
                    <p>
                        Use <strong>our cluster</strong> to intercept a demo servicefrom <strong>our sample app</strong> .
                        See Telepresence in action without committing any of your own resources.
                    </p>
                    <ol>
                        <li>Install the Telepresence CLI</li>
                        <li>Connect to the demo cluster</li>
                        <li>Intercept a service</li>
                    </ol>
                    <a id="tp-demo-option-a" href="qs-node" className="get-started-button">
                        Get Started
                        <svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
                            <path d="M13.3579 4.4545C12.9186 4.01517 12.2063 4.01517 11.7669 4.4545C11.3276 4.89384 11.3276 5.60615 11.7669 6.04549L16.5969 10.8755H4.68768C4.06636 10.8755 3.56268 11.3792 3.56268 12.0005C3.56268 12.6218 4.06636 13.1255 4.68768 13.1255H16.596L11.7669 17.9545C11.3276 18.3938 11.3276 19.1061 11.7669 19.5455C12.2063 19.9848 12.9186 19.9848 13.3579 19.5455L20.1079 12.7955C20.5473 12.3562 20.5473 11.6438 20.1079 11.2045L13.3579 4.4545Z"  />
                        </svg>
                    </a>
                </div>

                <div className="telepresence-choice">
                    <h2>
                      <mark className="highlight-mark">
                        Active
                      </mark>
                        Kubernetes User?
                    </h2>
                    <p>
                        Start using Telepresence in your own environment. Follow these steps to intercept <strong>your
                        service</strong> in <strong>your cluster</strong>.
                    </p>
                    <ol>
                        <li>Install the Telepresence CLI</li>
                        <li>Intercept your service</li>
                        <li>Create a preview URL</li>
                    </ol>
                    <a id="tp-intercepts-option-b" href="../howtos/intercepts/" className="get-started-button">
                        Get Started
                        <svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
                            <path d="M13.3579 4.4545C12.9186 4.01517 12.2063 4.01517 11.7669 4.4545C11.3276 4.89384 11.3276 5.60615 11.7669 6.04549L16.5969 10.8755H4.68768C4.06636 10.8755 3.56268 11.3792 3.56268 12.0005C3.56268 12.6218 4.06636 13.1255 4.68768 13.1255H16.596L11.7669 17.9545C11.3276 18.3938 11.3276 19.1061 11.7669 19.5455C12.2063 19.9848 12.9186 19.9848 13.3579 19.5455L20.1079 12.7955C20.5473 12.3562 20.5473 11.6438 20.1079 11.2045L13.3579 4.4545Z"  />
                        </svg>
                    </a>
                </div>
            </div>
            <div className="telepresence-choice">
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
    )
  }
}

export default TelepresenceQuickStartLanding
