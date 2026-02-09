import React, { useEffect, useRef, useState } from 'react';
import SwaggerUI from 'swagger-ui-react';
import 'swagger-ui-react/swagger-ui.css';

export const ApiDocs: React.FC = () => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [isLoaded, setIsLoaded] = useState(false);
  const [isDark, setIsDark] = useState(false);

  useEffect(() => {
    const root = document.documentElement;
    const updateTheme = () => {
      setIsDark(root.getAttribute('data-bs-theme') === 'dark');
    };

    updateTheme();
    const observer = new MutationObserver(updateTheme);
    observer.observe(root, { attributes: true, attributeFilter: ['data-bs-theme'] });

    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    if (isDark) {
      container.classList.add('swagger-dark');
    } else {
      container.classList.remove('swagger-dark');
    }
  }, [isDark, isLoaded]);

  return (
    <div className="container-fluid mt-2">
      <div className="card">
        <div className="card-header d-flex align-items-center">
          <h5 className="mb-0">API Documentation</h5>
          <span className="text-muted small ms-2">REST API reference</span>
        </div>
        <div ref={containerRef} className="card-body p-0 swagger-container">
          <SwaggerUI
            url="/api/docs/doc.json"
            onComplete={() => setIsLoaded(true)}
            docExpansion="list"
            defaultModelsExpandDepth={1}
            displayRequestDuration={true}
            filter={true}
            showExtensions={true}
            showCommonExtensions={true}
            tryItOutEnabled={true}
          />
        </div>
      </div>
    </div>
  );
};
