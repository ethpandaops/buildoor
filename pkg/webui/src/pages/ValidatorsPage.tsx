import React from 'react';
import { useValidators } from '../hooks/useValidators';
import { ValidatorList } from '../components/ValidatorList';

const ValidatorsPage: React.FC = () => {
  const { validators, loading } = useValidators();

  return (
    <ValidatorList validators={validators} loading={loading} fullPage={true} />
  );
};

export default ValidatorsPage;
