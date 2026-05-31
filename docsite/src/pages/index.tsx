import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={clsx('hero hero--primary', styles.heroBanner)}>
      <div className="container">
        <Heading as="h1" className="hero__title">
          {siteConfig.title}
        </Heading>
        <p className="hero__subtitle">{siteConfig.tagline}</p>
        <div className={styles.buttons}>
          <Link
            className="button button--secondary button--lg"
            to="/docs/quickstart">
            Get Started with Omni
          </Link>
        </div>
      </div>
    </header>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title={`Welcome to ${siteConfig.title}`}
      description="Advanced Agentic Coding Documentation">
      <HomepageHeader />
      <main className="flex flex-col items-center justify-center py-16">
        <div className="container text-center max-w-3xl">
          <Heading as="h2" className="text-3xl font-bold mb-6">
            The Future of Agentic Coding
          </Heading>
          <p className="text-xl opacity-80 mb-8">
            Omni brings highly advanced autonomous AI assistants directly into your codebase. Navigate the docs to explore architecture, learn how to configure your own agents, and dive into the API.
          </p>
          <Link
            className="button button--primary button--lg"
            to="/docs/quickstart">
            Read the Docs
          </Link>
        </div>
      </main>
    </Layout>
  );
}
